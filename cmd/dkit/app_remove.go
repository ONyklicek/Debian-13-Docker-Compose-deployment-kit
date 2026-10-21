package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func removeAppCommand(args []string, paths portable.Paths, output io.Writer) error {
	if len(args) == 0 || !appconfig.ValidName(args[0]) {
		return fmt.Errorf("usage: dkit app remove <name> --yes [--purge]")
	}
	name := args[0]
	confirmed, purge := false, false
	for _, arg := range args[1:] {
		switch arg {
		case "--yes", "-y":
			confirmed = true
		case "--purge":
			purge = true
		default:
			return fmt.Errorf("unknown app remove option %q; usage: dkit app remove <name> --yes [--purge]", arg)
		}
	}
	if !confirmed {
		return fmt.Errorf("app removal requires explicit confirmation: pass --yes")
	}
	if paths.Config == "" || paths.State == "" {
		return fmt.Errorf("portable config and state directories are required")
	}

	appDir := filepath.Join(paths.Config, "apps", name)
	configFile := filepath.Join(appDir, "app.env")
	if err := requireRegularAppConfig(filepath.Join(paths.Config, "apps"), appDir, configFile); err != nil {
		return err
	}
	stateDir := filepath.Join(paths.State, "apps", name)
	composeFile := filepath.Join(stateDir, "compose.yaml")
	if err := requireRegularFile(composeFile, "Compose model"); err != nil {
		return fmt.Errorf("cannot safely remove app %q: %w", name, err)
	}
	if err := requireDirectory(filepath.Join(paths.State, "apps"), "app state root"); err != nil {
		return fmt.Errorf("cannot safely remove app %q: %w", name, err)
	}
	if err := requireDirectory(stateDir, "app state directory"); err != nil {
		return fmt.Errorf("cannot safely remove app %q: %w", name, err)
	}
	composeArgs, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return fmt.Errorf("cannot safely remove app %q: %w", name, err)
	}
	composeArgs = append(composeArgs, "down", "--remove-orphans")
	if purge {
		composeArgs = append(composeArgs, "--volumes")
	}
	if err := runCommand("docker", composeArgs...); err != nil {
		return fmt.Errorf("stop app %q before removal: %w", name, err)
	}

	if purge {
		if err := os.RemoveAll(appDir); err != nil {
			return fmt.Errorf("remove app %q configuration after stopping containers: %w", name, err)
		}
		if err := os.RemoveAll(stateDir); err != nil {
			return fmt.Errorf("remove app %q state after stopping containers: %w", name, err)
		}
		_, err = fmt.Fprintf(output, "App %q was stopped and purged, including its Compose-managed volumes and local state. Docker images and shared services were not removed.\n", name)
		return err
	}

	archiveDir := filepath.Join(paths.Config, "apps-removed")
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return fmt.Errorf("create app archive directory %s: %w", archiveDir, err)
	}
	if err := requireDirectory(archiveDir, "app archive directory"); err != nil {
		return err
	}
	archivePath := filepath.Join(archiveDir, name+"."+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Rename(appDir, archivePath); err != nil {
		return fmt.Errorf("archive app %q configuration at %s: %w", name, archivePath, err)
	}
	_, err = fmt.Fprintf(output, "App %q was stopped and unregistered. Configuration was archived at %s; local state and Compose volumes were preserved.\n", name, archivePath)
	return err
}

func requireRegularAppConfig(appsDir, appDir, configFile string) error {
	if err := requireDirectory(appsDir, "app config root"); err != nil {
		return err
	}
	if err := requireDirectory(appDir, "app config directory"); err != nil {
		return err
	}
	return requireRegularFile(configFile, "app.env")
}

func requireDirectory(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s %s is not a real directory", label, path)
	}
	return nil
}

func requireRegularFile(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s %s is not a regular file", label, path)
	}
	return nil
}
