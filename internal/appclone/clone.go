package appclone

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

type Options struct {
	Source  string
	Name    string
	Domains []string
	NoEnv   bool
}

func Clone(configDir string, options Options) (createdDir string, retErr error) {
	if !appconfig.ValidName(options.Source) {
		return "", fmt.Errorf("invalid source app name %q", options.Source)
	}
	if !appconfig.ValidName(options.Name) {
		return "", fmt.Errorf("invalid destination app name %q", options.Name)
	}
	if options.Source == options.Name {
		return "", fmt.Errorf("source and destination app names must differ")
	}
	domains := strings.Join(splitDomains(options.Domains), ",")
	if domains == "" {
		return "", fmt.Errorf("at least one --domain is required")
	}

	source, err := appconfig.Read(configDir, options.Source)
	if err != nil {
		return "", err
	}
	if !source.Valid {
		return "", fmt.Errorf("source app %q configuration is invalid: %s", options.Source, strings.Join(source.Errors, "; "))
	}
	sourcePath, err := appconfig.AppFilePath(configDir, options.Source)
	if err != nil {
		return "", err
	}
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return "", fmt.Errorf("inspect source app config %s: %w", sourcePath, err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return "", fmt.Errorf("source app config %s is not a regular file", sourcePath)
	}
	sourceDir := filepath.Dir(sourcePath)
	overridePath := filepath.Join(sourceDir, "compose.override.yaml")
	if info, err := os.Lstat(overridePath); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("source app Compose override %s is not a regular file", overridePath)
		}
		return "", fmt.Errorf("source app %q has compose.override.yaml; portable Go Compose does not support overrides, so clone was refused", options.Source)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect source Compose override %s: %w", overridePath, err)
	}
	sshDir := filepath.Join(sourceDir, "ssh")
	if entries, err := os.ReadDir(sshDir); err == nil && len(entries) > 0 && source.Source == "git" {
		return "", fmt.Errorf("source app %q has SSH deployment credentials; portable Git builds do not support private SSH authentication, so clone was refused", options.Source)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect source SSH credentials %s: %w", sshDir, err)
	}

	appEnv, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read source app config %s: %w", sourcePath, err)
	}
	clonedEnv, err := appconfig.Rewrite(appEnv, map[string]string{"DOMAINS": domains}, []string{"DB_NAME", "DB_USER", "S3_BUCKET", "S3_DOMAIN", "PUBLISH_PORT"})
	if err != nil {
		return "", err
	}
	values, err := appconfig.Parse(clonedEnv)
	if err != nil {
		return "", fmt.Errorf("parse cloned app config: %w", err)
	}
	cloned := appconfig.Validate(options.Name, values)
	if !cloned.Valid {
		return "", fmt.Errorf("cloned app configuration is invalid: %s", strings.Join(cloned.Errors, "; "))
	}

	var env []byte
	envPath := filepath.Join(sourceDir, "env")
	if !options.NoEnv {
		info, err := os.Lstat(envPath)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("source app environment %s is not a regular file", envPath)
			}
			env, err = os.ReadFile(envPath)
			if err != nil {
				return "", fmt.Errorf("read source app environment %s: %w", envPath, err)
			}
			if _, err := appconfig.ParseEnv(env); err != nil {
				return "", fmt.Errorf("parse source app environment: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect source app environment %s: %w", envPath, err)
		}
	}

	appsDir := filepath.Join(configDir, "apps")
	if err := os.MkdirAll(appsDir, 0o700); err != nil {
		return "", fmt.Errorf("create apps directory: %w", err)
	}
	createdDir = filepath.Join(appsDir, options.Name)
	if _, err := os.Lstat(createdDir); err == nil {
		return "", fmt.Errorf("app %q already exists", options.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect destination app path %s: %w", createdDir, err)
	}
	stageDir, err := os.MkdirTemp(appsDir, ".dkit-clone-*")
	if err != nil {
		return "", fmt.Errorf("create temporary app directory: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			if err := os.RemoveAll(stageDir); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove incomplete app directory %s: %w", stageDir, err))
			}
		}
	}()
	if err := os.Chmod(stageDir, 0o700); err != nil {
		return "", fmt.Errorf("set permissions on temporary app directory: %w", err)
	}
	if err := os.Mkdir(filepath.Join(stageDir, "ssh"), 0o700); err != nil {
		return "", fmt.Errorf("create app SSH directory: %w", err)
	}
	if err := writeFile(filepath.Join(stageDir, "app.env"), clonedEnv, 0o640); err != nil {
		return "", err
	}
	if err := writeFile(filepath.Join(stageDir, "env"), env, 0o600); err != nil {
		return "", err
	}
	if _, err := os.Lstat(createdDir); err == nil {
		return "", fmt.Errorf("app %q already exists", options.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect destination app path %s: %w", createdDir, err)
	}
	if err := os.Rename(stageDir, createdDir); err != nil {
		if _, statErr := os.Lstat(createdDir); statErr == nil {
			return "", fmt.Errorf("app %q already exists", options.Name)
		}
		return "", fmt.Errorf("publish cloned app config atomically: %w", err)
	}
	failed = false
	return createdDir, nil
}

func splitDomains(values []string) []string {
	var domains []string
	for _, value := range values {
		domains = append(domains, strings.Fields(strings.ReplaceAll(value, ",", " "))...)
	}
	return domains
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}
