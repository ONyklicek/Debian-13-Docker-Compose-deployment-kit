package main

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

var appDomainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func domainCommand(args []string, configDir string, output io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dkit domain list|add|remove <app> [domain...]")
	}
	action, name := args[0], args[1]
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	domains := append([]string(nil), app.Domains...)
	switch action {
	case "list", "ls":
		if len(args) != 2 {
			return fmt.Errorf("usage: dkit domain list <app>")
		}
		for _, domain := range domains {
			if _, err := fmt.Fprintln(output, domain); err != nil {
				return err
			}
		}
		return nil
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: dkit domain add <app> <domain...>")
		}
		seen := make(map[string]bool, len(domains))
		for _, domain := range domains {
			seen[domain] = true
		}
		for _, domain := range args[2:] {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if !appDomainPattern.MatchString(domain) {
				return fmt.Errorf("invalid domain %q", domain)
			}
			if !seen[domain] {
				domains = append(domains, domain)
				seen[domain] = true
			}
		}
	case "remove", "rm":
		if len(args) < 3 {
			return fmt.Errorf("usage: dkit domain remove <app> <domain...>")
		}
		remove := make(map[string]bool, len(args)-2)
		for _, domain := range args[2:] {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if !appDomainPattern.MatchString(domain) {
				return fmt.Errorf("invalid domain %q", domain)
			}
			remove[domain] = true
		}
		filtered := domains[:0]
		for _, domain := range domains {
			if !remove[domain] {
				filtered = append(filtered, domain)
			}
		}
		domains = filtered
	default:
		return fmt.Errorf("usage: dkit domain list|add|remove <app> [domain...]")
	}
	updated, err := appconfig.Update(configDir, name, map[string]string{"DOMAINS": strings.Join(domains, ",")}, nil)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Updated domains for %s: %s. No Docker operations were performed.\n", name, strings.Join(updated.Domains, ", "))
	return err
}

func workerCommand(args []string, configDir string, output io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dkit worker list|add|remove <app> [worker] [options]")
	}
	action, name := args[0], args[1]
	app, err := appconfig.Read(configDir, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	workers := splitConfigList(app.Values["WORKERS"])
	switch action {
	case "list", "ls":
		if len(args) != 2 {
			return fmt.Errorf("usage: dkit worker list <app>")
		}
		sort.Strings(workers)
		for _, worker := range workers {
			if _, err := fmt.Fprintf(output, "%s\t%s\n", worker, app.Values["WORKER_"+worker+"_CMD"]); err != nil {
				return err
			}
		}
		return nil
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: dkit worker add <app> <name> --cmd <command> [--port <port>] [--path <path>] [--user <user>]")
		}
		worker := args[2]
		set := make(map[string]string)
		for i := 3; i < len(args); {
			if i+1 >= len(args) {
				return fmt.Errorf("worker option %s requires a value", args[i])
			}
			key := ""
			switch args[i] {
			case "--cmd":
				key = "CMD"
			case "--port":
				key = "PORT"
			case "--path":
				key = "PATH"
			case "--user":
				key = "USER"
			default:
				return fmt.Errorf("unknown worker option %q", args[i])
			}
			set["WORKER_"+worker+"_"+key] = args[i+1]
			i += 2
		}
		if strings.TrimSpace(set["WORKER_"+worker+"_CMD"]) == "" {
			return fmt.Errorf("worker %q requires a non-empty --cmd", worker)
		}
		found := false
		for _, existing := range workers {
			if existing == worker {
				found = true
				break
			}
		}
		if !found {
			workers = append(workers, worker)
		}
		set["WORKERS"] = strings.Join(workers, " ")
		if _, err := appconfig.Update(configDir, name, set, nil); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Updated worker %s for %s. Re-render Compose before applying; no Docker operations were performed.\n", worker, name)
		return err
	case "remove", "rm":
		if len(args) != 3 {
			return fmt.Errorf("usage: dkit worker remove <app> <name>")
		}
		worker := args[2]
		filtered := make([]string, 0, len(workers))
		found := false
		for _, existing := range workers {
			if existing == worker {
				found = true
				continue
			}
			filtered = append(filtered, existing)
		}
		if !found {
			return fmt.Errorf("worker %q is not configured for app %q", worker, name)
		}
		unset := []string{
			"WORKER_" + worker + "_CMD", "WORKER_" + worker + "_PORT",
			"WORKER_" + worker + "_PATH", "WORKER_" + worker + "_USER",
		}
		if _, err := appconfig.Update(configDir, name, map[string]string{"WORKERS": strings.Join(filtered, " ")}, unset); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Removed worker %s from %s configuration. Its container is unchanged until Compose is explicitly updated.\n", worker, name)
		return err
	default:
		return fmt.Errorf("usage: dkit worker list|add|remove <app> [worker] [options]")
	}
}

func splitConfigList(value string) []string {
	value = strings.ReplaceAll(value, ",", " ")
	var items []string
	for _, item := range strings.Fields(value) {
		items = append(items, item)
	}
	return items
}
