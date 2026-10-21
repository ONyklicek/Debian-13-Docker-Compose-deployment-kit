package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/buildinfo"
)

type appDetails struct {
	Name           string   `json:"name"`
	Error          string   `json:"error,omitempty"`
	Domains        []string `json:"domains"`
	Source         string   `json:"source"`
	Repo           string   `json:"repo,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	Image          string   `json:"image,omitempty"`
	Build          string   `json:"build"`
	Services       []string `json:"services"`
	Shared         []string `json:"shared"`
	Workers        []string `json:"workers"`
	Revision       string   `json:"revision,omitempty"`
	ConfigValid    bool     `json:"config_valid"`
	RuntimeChecked bool     `json:"runtime_checked"`
	RuntimeScope   string   `json:"runtime_scope"`
	Validation     string   `json:"validation_scope"`
}

func showApp(args []string, configDir, stateDir string, output io.Writer) error {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return fmt.Errorf("usage: dkit app show <name> [--json]")
	}
	name := args[0]
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return err
	}
	details := appDetails{
		Name: name, Domains: app.Domains, Source: app.Source, Build: app.Build,
		Services: app.Services, Shared: splitConfigList(app.Values["SHARED_SERVICES"]),
		Workers: splitConfigList(app.Values["WORKERS"]), ConfigValid: app.Valid,
		RuntimeChecked: false,
		RuntimeScope:   "configuration only; Docker containers, deployment slots, and public routing were not queried",
		Validation:     appConfigValidationScope,
	}
	details.Repo = app.Values["GIT_REPOSITORY"]
	details.Branch = app.Values["GIT_BRANCH"]
	details.Image = app.Values["IMAGE"]
	if !app.Valid {
		details.Error = strings.Join(app.Errors, "; ")
	}
	details.Revision, err = readBuildRevision(stateDir, name)
	if err != nil {
		return err
	}
	if len(args) == 2 && args[1] == "--json" {
		if err := json.NewEncoder(output).Encode(details); err != nil {
			return err
		}
		if !app.Valid {
			return fmt.Errorf("app %q configuration is invalid: %s", name, details.Error)
		}
		return nil
	}
	if _, err := fmt.Fprintf(output, "App:       %s\nDomains:   %s\nSource:    %s\nBuild:     %s\nServices:  %s\nWorkers:   %s\n",
		name, strings.Join(app.Domains, ", "), app.Source, app.Build,
		defaultText(strings.Join(app.Services, ", "), "-"), defaultText(strings.Join(details.Workers, ", "), "-")); err != nil {
		return err
	}
	if details.Repo != "" {
		if _, err := fmt.Fprintf(output, "Repository: %s\nBranch:     %s\n", details.Repo, defaultText(details.Branch, "(default)")); err != nil {
			return err
		}
	}
	if details.Image != "" {
		if _, err := fmt.Fprintf(output, "Image:      %s\n", details.Image); err != nil {
			return err
		}
	}
	if !app.Valid {
		if _, err := fmt.Fprintf(output, "Invalid:    %s\n", details.Error); err != nil {
			return err
		}
		return fmt.Errorf("app %q configuration is invalid", name)
	}
	_, err = fmt.Fprintf(output, "Revision:  %s\nRuntime:   not queried (configuration-only; no Docker operations were performed)\nValidation scope: %s\n",
		defaultText(details.Revision, "(none)"), appConfigValidationScope)
	return err
}

func readBuildRevision(stateDir, name string) (string, error) {
	if stateDir == "" {
		return "", nil
	}
	path := filepath.Join(stateDir, "apps", name, "build.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read build metadata for app %q: %w", name, err)
	}
	var info buildinfo.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return "", fmt.Errorf("parse build metadata for app %q: %w", name, err)
	}
	return info.Revision, nil
}
