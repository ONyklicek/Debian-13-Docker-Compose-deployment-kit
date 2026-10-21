package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/composehistory"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func appComposeRollback(name string, paths portable.Paths, output io.Writer) error {
	if paths.State == "" {
		return fmt.Errorf("state directory is not configured")
	}
	activeArgs, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return fmt.Errorf("refuse rollback for app %q because its active Compose model is not trusted: %w", name, err)
	}
	modelPath, err := composehistory.PreviousModel(paths.State, name)
	if err != nil {
		return err
	}
	activeModel, err := os.ReadFile(filepath.Join(paths.State, "apps", name, "compose.yaml"))
	if err != nil {
		return fmt.Errorf("read active Compose model for rollback recovery: %w", err)
	}
	activeSnapshot, err := writeRollbackSnapshot(paths.State, name, activeModel)
	if err != nil {
		return err
	}
	defer os.Remove(activeSnapshot)
	context, err := selectedContext(paths)
	if err != nil {
		return err
	}
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return err
	}
	args := contextArgs(context)
	args = append(args, "compose")
	servicesEnv := filepath.Join(filepath.Dir(appPath), "services.env")
	if info, err := os.Lstat(servicesEnv); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("app %q service credentials are not a regular file: %s", name, servicesEnv)
		}
		args = append(args, "--env-file", servicesEnv)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect app %q service credentials: %w", name, err)
	}
	args = append(args, "--project-name", "dkit-"+name, "--file", modelPath)
	overridePath, err := composeOverridePath(paths.Config, name)
	if err != nil {
		return err
	}
	if overridePath != "" {
		args = append(args, "--file", overridePath)
	}
	upArgs := append(append([]string(nil), args...), "up", "--detach", "--no-build", "--remove-orphans", "--wait", "--wait-timeout", "300")
	if err := runCommand("docker", upArgs...); err != nil {
		return restoreActiveCompose(name, withComposeFile(activeArgs, activeSnapshot), fmt.Errorf("Compose rollback failed: %w", err))
	}
	statusOutput, err := dockerOutput(args, "ps", "--all", "--format", "json")
	if err != nil {
		return restoreActiveCompose(name, withComposeFile(activeArgs, activeSnapshot), fmt.Errorf("verify rollback health: %w", err))
	}
	report, healthErr := inspectAppHealth(name, statusOutput)
	healthOutputErr := printAppHealth(output, report)
	if healthErr != nil {
		if healthOutputErr != nil {
			healthErr = fmt.Errorf("%w; write health report: %v", healthErr, healthOutputErr)
		}
		return restoreActiveCompose(name, withComposeFile(activeArgs, activeSnapshot), fmt.Errorf("rollback target did not pass its HTTP health check: %w", healthErr))
	}
	if err := composehistory.PromotePrevious(paths.State, name); err != nil {
		if writeErr := writeActiveModel(paths.State, name, activeModel); writeErr != nil {
			return fmt.Errorf("rollback state promotion failed (%v) and the active Compose file could not be restored: %w", err, writeErr)
		}
		if healthOutputErr != nil {
			err = fmt.Errorf("%w; write health report: %v", err, healthOutputErr)
		}
		return restoreActiveCompose(name, withComposeFile(activeArgs, activeSnapshot), fmt.Errorf("rollback was healthy but local model state could not be promoted: %w", err))
	}
	if healthOutputErr != nil {
		return healthOutputErr
	}
	_, err = fmt.Fprintf(output, "Rolled app %q back to its previous Compose model. This does not switch public traffic, restore database data, or undo migrations.\n", name)
	return err
}

func withComposeFile(args []string, composePath string) []string {
	result := append([]string(nil), args...)
	for i := 0; i+1 < len(result); i++ {
		if result[i] == "--file" {
			result[i+1] = composePath
			return result
		}
	}
	return result
}

func writeRollbackSnapshot(stateDir, name string, content []byte) (string, error) {
	dir := filepath.Join(stateDir, "apps", name)
	file, err := os.CreateTemp(dir, ".dkit-rollback-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create rollback recovery model: %w", err)
	}
	path := file.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return "", fmt.Errorf("write rollback recovery model: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close rollback recovery model: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("set rollback recovery model permissions: %w", err)
	}
	ok = true
	return path, nil
}

func writeActiveModel(stateDir, name string, content []byte) error {
	path := filepath.Join(stateDir, "apps", name, "compose.yaml")
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dkit-active-rollback-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary active Compose model: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary active Compose model: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary active Compose model: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("set active Compose model permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("restore active Compose model: %w", err)
	}
	return nil
}

func restoreActiveCompose(name string, activeArgs []string, rollbackErr error) error {
	restoreArgs := append(append([]string(nil), activeArgs...), "up", "--detach", "--no-build", "--remove-orphans", "--wait", "--wait-timeout", "300")
	if err := runCommand("docker", restoreArgs...); err != nil {
		return fmt.Errorf("Compose rollback for app %q failed (%v), and restoring the previously active model also failed: %w", name, rollbackErr, err)
	}
	status, err := dockerOutput(activeArgs, "ps", "--all", "--format", "json")
	if err != nil {
		return fmt.Errorf("Compose rollback for app %q failed (%v); restoring the previously active model could not be verified: %w", name, rollbackErr, err)
	}
	_, err = inspectAppHealth(name, status)
	if err != nil {
		return fmt.Errorf("Compose rollback for app %q failed (%v); previously active model is not healthy after recovery: %w", name, rollbackErr, err)
	}
	return fmt.Errorf("Compose rollback for app %q failed (%v); re-applied the previously active model and verified it healthy", name, rollbackErr)
}
