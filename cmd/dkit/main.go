package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appclone"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appcreate"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/composegen"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/composehistory"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/presetcatalog"
)

type settings struct {
	DockerContext string `json:"docker_context,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dkit:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	paths, err := portable.ResolvePaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "help", "-h", "--help":
		return usage()
	case "version", "--version":
		fmt.Println("dkit portable CLI (development build)")
		return nil
	case "paths":
		return printPaths(args[1:], paths)
	case "context", "contexts":
		return contextCommand(args[1:], paths)
	case "docker":
		return dockerCommand(args[1:], paths)
	case "compose":
		return composeCommand(args[1:], paths)
	case "status":
		return statusCommand(args[1:], paths)
	case "app":
		if len(args) > 1 && args[1] == "build" {
			return buildApp(args[2:], paths)
		}
		return appCommand(args[1:], paths, os.Stdout)
	case "apps", "ls":
		return listApps(args[1:], paths.Config, os.Stdout)
	case "env":
		return envCommand(args[1:], paths.Config, os.Stdout)
	case "domain", "domains":
		return domainCommand(args[1:], paths.Config, os.Stdout)
	case "worker", "workers":
		return workerCommand(args[1:], paths.Config, os.Stdout)
	case "db":
		return dbCommand(args[1:], paths)
	case "logs":
		return logsCommand(args[1:], paths)
	case "exec", "sh":
		return execAppCommand(args[1:], paths)
	case "backup":
		return backupCommand(args[1:], paths)
	case "preset":
		return presetCommand(args[1:], paths.Config, os.Stdout)
	case "doctor":
		return doctor(paths)
	default:
		return runLegacy(args, paths)
	}
}

func usage() error {
	fmt.Println(`dkit — portable Docker deployment kit CLI

Portable commands:
  dkit paths [--json]                 show per-user config and state paths
  dkit context list [--json]          discover Docker contexts
  dkit context current                show the selected Docker context
  dkit context use <name>             select a context for dkit commands
  dkit docker <docker arguments...>   run Docker with the selected context
  dkit compose <compose arguments...> run Compose with the selected context
  dkit status [--json]                show Docker daemon and container status
  dkit app create <name> --preset <preset> --domain <domain> [--source git|image] [--repo <url> | --image <image>]
  dkit app clone <source> <name> --domain <domain> [--no-env] clone app config without service credentials
  dkit app remove <name> --yes [--purge] stop and unregister; purge also removes app state/volumes
  dkit app build <name> [--ref <git-ref>] build a Git app image without deploying it
  dkit app list [--json]              discover configured apps
  dkit app show <name> [--json]       show app config without querying runtime
  dkit app check <name> [--json]      validate app.env settings without Docker
  dkit app compose render <name> [--write] render a supported app Compose model
  dkit app compose <config|apply|up|down|start|stop|restart|ps|health|rollback|logs|exec> <name>
                                      manage a rendered app Compose project
  dkit domain <list|add|remove> <app> [domain...]
                                      manage app domains in app.env
  dkit worker <list|add|remove> <app> [worker] [options]
                                      manage worker settings in app.env
  dkit app get <name> [key|--json]    read app.env settings
  dkit app set <name> KEY=VALUE...    update validated app.env settings
  dkit env list <name> [--show|--json] read app environment (secrets masked by default)
  dkit env set <name> KEY=VALUE...   update app environment
  dkit env unset <name> <key...>     remove app environment keys
  dkit db shell|dump|import|migrate <name> manage an app's dedicated database
  dkit logs <name> [service...] [-f] [--tail N] show logs for a rendered app
  dkit exec <name> [service] [-- command...] execute in a rendered app service
  dkit backup <name> | backup list [name] | backup restore <name> [snapshot] --target <dir>
  dkit preset list [--json]           list built-in and user presets
  dkit doctor                         check Docker CLI, Compose, and daemon

Existing deployment commands are forwarded to the bundled Bash engine on
Linux. Preset mutations and deployment remain Debian/Linux-oriented; see the README for status.`)
	return nil
}

func printPaths(args []string, paths portable.Paths) error {
	if len(args) == 1 && args[0] == "--json" {
		return json.NewEncoder(os.Stdout).Encode(paths)
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: dkit paths [--json]")
	}
	fmt.Printf("Config: %s\nState:  %s\n", paths.Config, paths.State)
	return nil
}

func contextCommand(args []string, paths portable.Paths) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dkit context list|current|use")
	}
	switch args[0] {
	case "list":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return fmt.Errorf("usage: dkit context list [--json]")
		}
		contexts, err := listContexts()
		if err != nil {
			return err
		}
		if len(args) == 2 && args[1] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(contexts)
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: dkit context list [--json]")
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tENDPOINT\tDESCRIPTION")
		for _, ctx := range contexts {
			name := ctx.Name
			if ctx.Current {
				name += " *"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", name, ctx.Endpoint, ctx.Description)
		}
		return w.Flush()
	case "current":
		if len(args) != 1 {
			return fmt.Errorf("usage: dkit context current")
		}
		ctx, err := selectedContext(paths)
		if err != nil {
			return err
		}
		fmt.Println(ctx)
		return nil
	case "use":
		if len(args) != 2 || strings.TrimSpace(args[1]) == "" {
			return fmt.Errorf("usage: dkit context use <name>")
		}
		contexts, err := listContexts()
		if err != nil {
			return err
		}
		found := false
		for _, ctx := range contexts {
			if ctx.Name == args[1] {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Docker context %q not found; run `dkit context list`", args[1])
		}
		cfg, err := loadSettings(paths.Config)
		if err != nil {
			return err
		}
		cfg.DockerContext = args[1]
		if err := saveSettings(paths.Config, cfg); err != nil {
			return err
		}
		fmt.Printf("Selected Docker context: %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown context command %q; use list, current, or use", args[0])
	}
}

func listContexts() ([]portable.Context, error) {
	out, err := dockerOutput(nil, "context", "ls", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return portable.ParseContexts(out)
}

func dockerCommand(args []string, paths portable.Paths) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dkit docker <docker arguments...>")
	}
	context, err := selectedContext(paths)
	if err != nil {
		return err
	}
	dockerArgs := contextArgs(context)
	dockerArgs = append(dockerArgs, args...)
	return runCommand("docker", dockerArgs...)
}

func composeCommand(args []string, paths portable.Paths) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dkit compose <compose arguments...>")
	}
	context, err := selectedContext(paths)
	if err != nil {
		return err
	}
	dockerArgs := contextArgs(context)
	dockerArgs = append(dockerArgs, "compose")
	dockerArgs = append(dockerArgs, args...)
	return runCommand("docker", dockerArgs...)
}

type daemonStatus struct {
	Context           string            `json:"context"`
	Name              string            `json:"name"`
	ServerVersion     string            `json:"server_version"`
	OperatingSystem   string            `json:"operating_system"`
	OSType            string            `json:"os_type"`
	Architecture      string            `json:"architecture"`
	Containers        int               `json:"containers"`
	ContainersRunning int               `json:"containers_running"`
	ContainersPaused  int               `json:"containers_paused"`
	ContainersStopped int               `json:"containers_stopped"`
	ContainerList     []containerStatus `json:"container_list"`
}

type containerStatus struct {
	ID     string `json:"id"`
	Names  string `json:"names"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
}

type dockerInfo struct {
	Name              string `json:"Name"`
	ServerVersion     string `json:"ServerVersion"`
	OperatingSystem   string `json:"OperatingSystem"`
	OSType            string `json:"OSType"`
	Architecture      string `json:"Architecture"`
	Containers        int    `json:"Containers"`
	ContainersRunning int    `json:"ContainersRunning"`
	ContainersPaused  int    `json:"ContainersPaused"`
	ContainersStopped int    `json:"ContainersStopped"`
}

func statusCommand(args []string, paths portable.Paths) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
		return fmt.Errorf("usage: dkit status [--json]")
	}
	context, err := selectedContext(paths)
	if err != nil {
		return err
	}
	status, err := collectStatus(context, func(args ...string) ([]byte, error) {
		return dockerOutput(nil, args...)
	})
	if err != nil {
		return err
	}
	status.Context = defaultText(context, "(Docker default)")
	if len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	return printStatus(os.Stdout, status)
}

const appConfigValidationScope = "Static app.env checks only; Compose rendering, Docker state, env/services credentials, state.env, and deployment readiness are not checked."

type appCheckResult struct {
	Name   string   `json:"name"`
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
	Scope  string   `json:"validation_scope"`
}

func appCommand(args []string, paths portable.Paths, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dkit app create <name> --preset <preset> --domain <domain> [options] | list [--json] | check <name> [--json]")
	}
	switch args[0] {
	case "create":
		return createApp(args[1:], paths.Config, output)
	case "clone":
		return cloneApp(args[1:], paths.Config, output)
	case "remove", "rm", "delete":
		return removeAppCommand(args[1:], paths, output)
	case "show", "info":
		return showApp(args[1:], paths.Config, paths.State, output)
	case "list", "ls":
		return listApps(args[1:], paths.Config, output)
	case "check", "validate":
		return checkApp(args[1:], paths.Config, output)
	case "compose":
		return appComposeCommand(args[1:], paths, output)
	case "get":
		return getApp(args[1:], paths.Config, output)
	case "set":
		return setApp(args[1:], paths.Config, output)
	default:
		return runLegacy(append([]string{"app"}, args...), paths)
	}
}

func cloneApp(args []string, configDir string, output io.Writer) error {
	if len(args) < 3 {
		return fmt.Errorf("usage: dkit app clone <source> <name> --domain <domain> [--no-env]")
	}
	options := appclone.Options{Source: args[0], Name: args[1]}
	for i := 2; i < len(args); {
		switch args[i] {
		case "--domain", "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("%s requires a domain", args[i])
			}
			options.Domains = append(options.Domains, args[i+1])
			i += 2
		case "--no-env":
			options.NoEnv = true
			i++
		default:
			return fmt.Errorf("unknown app clone option %q", args[i])
		}
	}
	if len(options.Domains) == 0 {
		return fmt.Errorf("at least one --domain is required")
	}
	path, err := appclone.Clone(configDir, options)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Cloned app %s to %s with fresh service credentials. No Docker operations were performed.\n", options.Source, path); err != nil {
		return err
	}
	if !options.NoEnv {
		_, err = fmt.Fprintf(output, "The source env file, including secrets, was copied. Review it and rotate app-specific keys such as APP_KEY before deployment.\n")
	}
	return err
}

func appComposeCommand(args []string, paths portable.Paths, output io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dkit app compose render <name> [--write] | <config|apply|up|down|start|stop|restart|ps|health|rollback|logs|exec> <name>")
	}
	if args[0] != "render" {
		return appComposeOperation(args[0], args[1], args[2:], paths, output)
	}
	if len(args) > 3 || (len(args) == 3 && args[2] != "--write") {
		return fmt.Errorf("usage: dkit app compose render <name> [--write]")
	}
	name := args[1]
	if len(args) == 3 {
		if paths.State == "" {
			return fmt.Errorf("state directory is not configured")
		}
		if err := composegen.Prepare(paths.Config, paths.State, name); err != nil {
			return err
		}
	}
	compose, err := composegen.Render(paths.Config, paths.State, name)
	if err != nil {
		return err
	}
	if len(args) == 3 {
		path := filepath.Join(paths.State, "apps", name, "compose.yaml")
		if err := composehistory.WriteRendered(paths.State, name, compose); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Wrote Compose model to %s. No Docker operations were performed.\n", path)
		return err
	}
	_, err = output.Write(compose)
	return err
}

func appComposeOperation(action, name string, extra []string, paths portable.Paths, output io.Writer) error {
	switch action {
	case "config", "apply", "up", "down", "start", "stop", "restart", "ps", "health", "rollback", "logs", "exec":
	default:
		return fmt.Errorf("unknown app Compose action %q", action)
	}
	switch action {
	case "config", "apply", "up", "down", "start", "stop", "restart", "rollback":
		if len(extra) != 0 {
			return fmt.Errorf("dkit app compose %s does not accept extra arguments", action)
		}
	case "ps", "health":
		if action == "health" && len(extra) == 1 && extra[0] == "--json" {
			break
		}
		if len(extra) != 0 {
			return fmt.Errorf("usage: dkit app compose %s <name>", action)
		}
	case "logs":
		if len(extra) != 0 {
			return fmt.Errorf("usage: dkit app compose logs <name>")
		}
	case "exec":
		if len(extra) > 0 && extra[0] == "--" {
			extra = extra[1:]
		}
	}
	if action == "rollback" {
		return appComposeRollback(name, paths, output)
	}
	if action == "apply" {
		if err := appComposeCommand([]string{"render", name, "--write"}, paths, io.Discard); err != nil {
			return fmt.Errorf("render app %q configuration before apply: %w", name, err)
		}
	}
	args, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	if action == "health" {
		data, err := dockerOutput(args, "ps", "--all", "--format", "json")
		if err != nil {
			return fmt.Errorf("read app %q Compose health: %w", name, err)
		}
		report, healthErr := inspectAppHealth(name, data)
		if len(extra) == 1 && extra[0] == "--json" {
			if err := json.NewEncoder(output).Encode(report); err != nil {
				return err
			}
			return healthErr
		}
		if err := printAppHealth(output, report); err != nil {
			return err
		}
		return healthErr
	}
	if action == "config" {
		args = append(args, "config", "--quiet")
		if err := runCommand("docker", args...); err != nil {
			return fmt.Errorf("app %q Compose configuration is invalid: %w", name, err)
		}
		_, err = fmt.Fprintf(output, "Compose configuration for app %q is valid. No Docker state was changed.\n", name)
		return err
	}
	composeAction := action
	if composeAction == "apply" {
		composeAction = "up"
	}
	args = append(args, composeAction)
	switch composeAction {
	case "up":
		args = append(args, "--detach", "--no-build", "--remove-orphans", "--wait", "--wait-timeout", "300")
	case "down":
		args = append(args, "--remove-orphans")
	case "ps":
		args = append(args, "--all")
	case "exec":
		args = append(args, "-T", "app")
		if len(extra) == 0 {
			extra = []string{"sh"}
		}
		args = append(args, extra...)
	}
	if err := runCommand("docker", args...); err != nil {
		return fmt.Errorf("app %q Compose %s failed: %w", name, action, err)
	}
	if action == "up" || action == "apply" {
		if action == "apply" {
			_, err = fmt.Fprintln(output, "Current app configuration was rendered and applied. Services may have been recreated; this does not build Git images or switch public traffic.")
			return err
		}
		_, err = fmt.Fprintln(output, "Compose services passed their health checks. This is not a blue/green traffic switch.")
	}
	return err
}

func appComposeProjectArgs(name string, paths portable.Paths) ([]string, error) {
	if !appconfig.ValidName(name) {
		return nil, fmt.Errorf("invalid app name %q", name)
	}
	if paths.State == "" {
		return nil, fmt.Errorf("state directory is not configured")
	}
	composePath := filepath.Join(paths.State, "apps", name, "compose.yaml")
	info, err := os.Lstat(composePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("Compose model for app %q not found at %s; run `dkit app compose render %s --write` first", name, composePath, name)
		}
		return nil, fmt.Errorf("inspect Compose model for app %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Compose model for app %q is not a regular file: %s", name, composePath)
	}
	context, err := selectedContext(paths)
	if err != nil {
		return nil, err
	}
	rendered, err := composegen.RenderForContext(paths.Config, paths.State, name, context)
	if err != nil {
		return nil, err
	}
	onDisk, err := os.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("read Compose model for app %q: %w", name, err)
	}
	if !composehistory.VerifyActive(paths.State, name, rendered, onDisk) {
		return nil, fmt.Errorf("Compose model for app %q is stale or was edited; run `dkit app compose render %s --write` again", name, name)
	}
	runtimeEnv := filepath.Join(paths.State, "apps", name, "runtime.env")
	runtimeInfo, err := os.Lstat(runtimeEnv)
	if err != nil {
		return nil, fmt.Errorf("runtime environment for app %q is not prepared; run `dkit app compose render %s --write` first: %w", name, name, err)
	}
	if !runtimeInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("runtime environment for app %q is not a regular file: %s", name, runtimeEnv)
	}
	args := contextArgs(context)
	args = append(args, "compose")
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return nil, err
	}
	servicesEnv := filepath.Join(filepath.Dir(appPath), "services.env")
	if envInfo, statErr := os.Lstat(servicesEnv); statErr == nil {
		if !envInfo.Mode().IsRegular() {
			return nil, fmt.Errorf("app %q service credentials are not a regular file: %s", name, servicesEnv)
		}
		args = append(args, "--env-file", servicesEnv)
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect app %q service credentials %s: %w", name, servicesEnv, statErr)
	}
	args = append(args, "--project-name", "dkit-"+name, "--file", composePath)
	return args, nil
}

func dbCommand(args []string, paths portable.Paths) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dkit db shell|dump|migrate <name> | dkit db import <name> --yes")
	}
	action, name := args[0], args[1]
	confirmedImport := action == "import" && len(args) == 3 && args[2] == "--yes"
	confirmedMigrate := action == "migrate" && len(args) == 3 && args[2] == "--yes"
	if action == "import" && !confirmedImport {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"db"}, args...), paths)
		}
		return fmt.Errorf("database import requires explicit confirmation: `dkit db import %s --yes` with SQL supplied on stdin", name)
	}
	if action == "migrate" && !confirmedMigrate {
		return fmt.Errorf("database migration requires explicit confirmation: `dkit db migrate %s --yes`", name)
	}
	if action != "shell" && action != "dump" && action != "import" && action != "migrate" {
		return runLegacy(append([]string{"db"}, args...), paths)
	}
	if (action == "import" && !confirmedImport) || (action == "migrate" && !confirmedMigrate) ||
		((action != "import" && action != "migrate") && len(args) != 2) {
		return fmt.Errorf("usage: dkit db shell|dump|migrate <name> | dkit db import <name> --yes")
	}
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if action == "migrate" {
		return dbMigrateCommand(name, app, paths)
	}
	if app.Values["SOURCE"] != "image" {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"db"}, args...), paths)
		}
		return fmt.Errorf("app %q uses a Git build; portable DB access currently supports rendered image apps only", name)
	}
	if app.Values["SHARED_SERVICES"] != "" {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"db"}, args...), paths)
		}
		return fmt.Errorf("app %q uses shared services; portable database access currently supports dedicated services only", name)
	}
	database := ""
	for _, service := range app.Services {
		if service == "mariadb" || service == "postgres" {
			database = service
			break
		}
	}
	if database == "" {
		return fmt.Errorf("app %q has no dedicated MariaDB or PostgreSQL service", name)
	}
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return err
	}
	servicesEnv := filepath.Join(filepath.Dir(appPath), "services.env")
	servicesInfo, err := os.Lstat(servicesEnv)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("app %q database credentials are not prepared; run `dkit app compose render %s --write` first", name, name)
		}
		return fmt.Errorf("inspect app %q database credentials: %w", name, err)
	}
	if !servicesInfo.Mode().IsRegular() {
		return fmt.Errorf("app %q database credentials are not a regular file: %s", name, servicesEnv)
	}
	credentialData, err := os.ReadFile(servicesEnv)
	if err != nil {
		return fmt.Errorf("read app %q database credentials: %w", name, err)
	}
	credentials, err := appconfig.ParseEnv(credentialData)
	if err != nil {
		return fmt.Errorf("parse app %q database credentials: %w", name, err)
	}
	requiredCredentials := []string{"POSTGRES_PASSWORD"}
	if database == "mariadb" {
		requiredCredentials = []string{"MARIADB_PASSWORD", "MARIADB_ROOT_PASSWORD"}
	}
	for _, key := range requiredCredentials {
		if credentials[key] == "" {
			return fmt.Errorf("app %q is missing %s in services.env; run `dkit app compose render %s --write` to initialize credentials", name, key, name)
		}
	}
	command, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	args = command
	args = append(args, "exec", "-T")
	if action == "shell" {
		args = append(args, "-i", database, "sh", "-c", dbShellCommand(database))
	} else {
		args = append(args, "-i", database, "sh", "-c", dbDumpCommand(database))
		if action == "import" {
			args[len(args)-1] = dbImportCommand(database)
		}
	}
	return runCommand("docker", args...)
}

func dbMigrateCommand(name string, app appconfig.App, paths portable.Paths) error {
	migration := strings.TrimSpace(app.Values["MIGRATE_CMD"])
	if migration == "" {
		return fmt.Errorf("app %q has no MIGRATE_CMD configured", name)
	}
	command, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	command = append(command, "run", "--rm", "-T")
	if user := app.Values["MIGRATE_USER"]; user != "" {
		command = append(command, "--user", user)
	}
	command = append(command, "app", "sh", "-c", migration)
	if err := runCommand("docker", command...); err != nil {
		return fmt.Errorf("database migration for app %q failed: %w", name, err)
	}
	fmt.Printf("Database migration completed for app %s.\n", name)
	return nil
}

func dbShellCommand(database string) string {
	if database == "mariadb" {
		return `exec mariadb -u "$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE"`
	}
	return `export PGPASSWORD="$POSTGRES_PASSWORD"; exec psql -U "$POSTGRES_USER" "$POSTGRES_DB"`
}

func dbDumpCommand(database string) string {
	if database == "mariadb" {
		return `exec mariadb-dump --single-transaction --routines --events --triggers --hex-blob --no-tablespaces -u "$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE"`
	}
	return `export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_dump --no-owner -U "$POSTGRES_USER" "$POSTGRES_DB"`
}

func dbImportCommand(database string) string {
	if database == "mariadb" {
		return `exec mariadb -u "$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE"`
	}
	return `exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$POSTGRES_DB"`
}

func logsCommand(args []string, paths portable.Paths) error {
	if len(args) == 0 || !appconfig.ValidName(args[0]) {
		return fmt.Errorf("usage: dkit logs <name> [service...] [-f] [--tail N]")
	}
	name := args[0]
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if app.Values["SOURCE"] != "image" {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"logs"}, args...), paths)
		}
		return fmt.Errorf("app %q uses a Git build; portable logs currently support rendered image apps only", name)
	}
	tail := "200"
	follow := false
	var requested []string
	for i := 1; i < len(args); {
		switch args[i] {
		case "-f", "--follow":
			follow = true
			i++
		case "--tail", "-n":
			if i+1 >= len(args) || !decimalArgument(args[i+1]) {
				return fmt.Errorf("--tail requires a non-negative integer")
			}
			tail = args[i+1]
			i += 2
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown logs option %q", args[i])
			}
			requested = append(requested, args[i])
			i++
		}
	}
	for _, service := range requested {
		if !appServiceExists(app, service) {
			return fmt.Errorf("app %q has no Compose service %q", name, service)
		}
	}
	command, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	command = append(command, "logs", "--timestamps", "--tail="+tail)
	if follow {
		command = append(command, "--follow")
	}
	command = append(command, requested...)
	return runCommand("docker", command...)
}

func execAppCommand(args []string, paths portable.Paths) error {
	if len(args) == 0 || !appconfig.ValidName(args[0]) {
		return fmt.Errorf("usage: dkit exec <name> [service] [-- command...]")
	}
	name, service := args[0], "app"
	commandArgs := args[1:]
	if len(commandArgs) > 0 && commandArgs[0] != "--" {
		service = commandArgs[0]
		commandArgs = commandArgs[1:]
	}
	if len(commandArgs) > 0 && commandArgs[0] == "--" {
		commandArgs = commandArgs[1:]
	}
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if app.Values["SOURCE"] != "image" {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"exec"}, args...), paths)
		}
		return fmt.Errorf("app %q uses a Git build; portable exec currently supports rendered image apps only", name)
	}
	if !appServiceExists(app, service) {
		return fmt.Errorf("app %q has no Compose service %q", name, service)
	}
	if len(commandArgs) == 0 {
		commandArgs = []string{"sh"}
	}
	command, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	command = append(command, "exec", "-T", "-i", service)
	command = append(command, commandArgs...)
	return runCommand("docker", command...)
}

func appServiceExists(app appconfig.App, name string) bool {
	if name == "app" {
		return true
	}
	for _, worker := range strings.Fields(strings.ReplaceAll(app.Values["WORKERS"], ",", " ")) {
		if worker == name {
			return true
		}
	}
	for _, service := range app.Services {
		if service == name && service != "s3" {
			return true
		}
	}
	return false
}

func decimalArgument(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func backupCommand(args []string, paths portable.Paths) error {
	if len(args) > 0 && args[0] == "restore" {
		return backupRestoreCommand(args[1:], paths)
	}
	if len(args) > 0 && args[0] == "list" {
		if len(args) > 2 {
			return fmt.Errorf("usage: dkit backup list [name]")
		}
		resticEnv, err := loadResticEnvironment(paths.Config)
		if err != nil {
			return err
		}
		resticArgs := []string{"snapshots"}
		if len(args) == 2 {
			if !appconfig.ValidName(args[1]) {
				return fmt.Errorf("invalid app name %q", args[1])
			}
			resticArgs = append(resticArgs, "--tag", "dkit:"+args[1])
		}
		return runCommandWithEnv("restic", resticEnv, resticArgs...)
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "--all") {
		if runtime.GOOS == "linux" {
			return runLegacy(append([]string{"backup"}, args...), paths)
		}
		return fmt.Errorf("portable backup supports one rendered image app at a time; bulk/platform backup still requires the Linux/Bash engine")
	}
	if args[0] == "--all" {
		return fmt.Errorf("usage: dkit backup <name> | dkit backup list [name] | dkit backup --all")
	}
	if len(args) != 1 || !appconfig.ValidName(args[0]) {
		return fmt.Errorf("usage: dkit backup <name> | dkit backup list [name] | dkit backup --all")
	}
	return backupImageApp(args[0], paths)
}

func backupRestoreCommand(args []string, paths portable.Paths) error {
	if len(args) < 3 || len(args) > 4 || !appconfig.ValidName(args[0]) {
		return fmt.Errorf("usage: dkit backup restore <name> [snapshot] --target <new-directory>")
	}
	name, snapshot := args[0], "latest"
	targetIndex := 1
	if len(args) == 4 {
		snapshot = args[1]
		targetIndex = 2
	}
	if args[targetIndex] != "--target" || !validSnapshotSelector(snapshot) {
		return fmt.Errorf("usage: dkit backup restore <name> [snapshot] --target <new-directory>")
	}
	target, err := filepath.Abs(args[targetIndex+1])
	if err != nil {
		return fmt.Errorf("resolve restore target: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("restore target %s already exists; choose a new empty directory", target)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect restore target %s: %w", target, err)
	}
	parent := filepath.Dir(target)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("inspect restore target parent %s: %w", parent, err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("restore target parent %s is not a directory", parent)
	}
	resticEnv, err := loadResticEnvironment(paths.Config)
	if err != nil {
		return err
	}
	host, err := backupHost(resticEnv)
	if err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return fmt.Errorf("create restore staging directory %s: %w", target, err)
	}
	resticArgs := []string{"restore", snapshot, "--tag", "dkit:" + name, "--host", host, "--target", target}
	if err := runCommandWithEnv("restic", resticEnv, resticArgs...); err != nil {
		return fmt.Errorf("%w; partial restored files, if any, remain in %s for inspection", err, target)
	}
	fmt.Printf("Restored snapshot %s for app %s into staging directory %s. Files were not copied into the active app and no Docker operations were run.\n", snapshot, name, target)
	return nil
}

func validSnapshotSelector(snapshot string) bool {
	if snapshot == "" || strings.HasPrefix(snapshot, "-") {
		return false
	}
	for _, char := range snapshot {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func backupImageApp(name string, paths portable.Paths) (returnErr error) {
	app, err := appconfig.Read(paths.Config, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	if app.Values["SOURCE"] != "image" {
		if runtime.GOOS == "linux" {
			return runLegacy([]string{"backup", name}, paths)
		}
		return fmt.Errorf("app %q uses a Git build; portable backup currently supports rendered image apps only", name)
	}
	resticEnv, err := loadResticEnvironment(paths.Config)
	if err != nil {
		return err
	}
	composeArgs, err := appComposeProjectArgs(name, paths)
	if err != nil {
		return err
	}
	appPath, err := appconfig.AppFilePath(paths.Config, name)
	if err != nil {
		return err
	}
	serviceEnvPath := filepath.Join(filepath.Dir(appPath), "services.env")
	serviceData, err := os.ReadFile(serviceEnvPath)
	if err != nil {
		return fmt.Errorf("read app %q service credentials: %w", name, err)
	}
	services, err := appconfig.ParseEnv(serviceData)
	if err != nil {
		return err
	}
	var databases []string
	for _, service := range app.Services {
		if service == "mariadb" || service == "postgres" {
			databases = append(databases, service)
		}
	}
	if len(databases) == 0 {
		return fmt.Errorf("app %q has no dedicated MariaDB/PostgreSQL database to back up", name)
	}
	for _, database := range databases {
		keys := []string{"POSTGRES_PASSWORD"}
		if database == "mariadb" {
			keys = []string{"MARIADB_PASSWORD", "MARIADB_ROOT_PASSWORD"}
		}
		for _, key := range keys {
			if services[key] == "" {
				return fmt.Errorf("app %q is missing %s in services.env; run `dkit app compose render %s --write` first", name, key, name)
			}
		}
	}
	host, err := backupHost(resticEnv)
	if err != nil {
		return err
	}
	stateAppDir := filepath.Join(paths.State, "apps", name)
	dumpDir := filepath.Join(stateAppDir, "backup-dumps")
	if err := os.Mkdir(dumpDir, 0o700); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("temporary database dump directory %s already exists; inspect and remove it before retrying", dumpDir)
		}
		return fmt.Errorf("create temporary database dump directory %s: %w", dumpDir, err)
	}
	defer func() {
		if err := os.RemoveAll(dumpDir); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary database dump directory %s: %w", dumpDir, err))
		}
	}()
	pathsToBackup := []string{
		filepath.Join(filepath.Dir(appPath)),
		stateAppDir,
	}
	volumeSnapshotDir, err := backupNamedVolumes(name, app, stateAppDir, composeArgs)
	if err != nil {
		return err
	}
	if volumeSnapshotDir != "" {
		defer func() {
			if err := os.RemoveAll(volumeSnapshotDir); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove temporary Docker volume snapshots %s: %w", volumeSnapshotDir, err))
			}
		}()
	}
	for _, database := range databases {
		dumpPath := filepath.Join(dumpDir, database+".sql")
		if err := dumpDatabase(name, database, dumpPath, composeArgs); err != nil {
			return err
		}
	}
	resticArgs := []string{"backup", "--tag", "dkit:" + name, "--host", host}
	resticArgs = append(resticArgs, pathsToBackup...)
	if err := runCommandWithEnv("restic", resticEnv, resticArgs...); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, "Backup contains app configuration, dedicated SQL dumps, and read-only archives of existing Compose named volumes. Volume archives are not application-consistent guarantees; shared services, S3, and container writable layers are not included. No retention pruning was run.")
	return err
}

func backupNamedVolumes(name string, app appconfig.App, stateAppDir string, composeArgs []string) (string, error) {
	expected := make(map[string]bool)
	for _, service := range app.Services {
		switch service {
		case "mariadb", "postgres", "redis":
			expected[service+"_data"] = true
		}
	}
	for _, entry := range strings.Fields(app.Values["PERSIST"]) {
		volumeName, _, _ := strings.Cut(entry, ":")
		expected["persist_"+volumeName] = true
	}
	if len(expected) == 0 {
		return "", nil
	}

	var contextPrefix []string
	for _, arg := range composeArgs {
		if arg == "compose" {
			break
		}
		contextPrefix = append(contextPrefix, arg)
	}
	listArgs := append(append([]string(nil), contextPrefix...), "volume", "ls",
		"--filter", "label=com.docker.compose.project=dkit-"+name, "--format", "{{.Name}}")
	cmd := exec.Command("docker", listArgs...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("list Docker volumes for app %q: %w", name, err)
	}
	existing := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		volume := strings.TrimSpace(line)
		if volume != "" {
			existing[volume] = true
		}
	}
	expectedKeys := make([]string, 0, len(expected))
	for key := range expected {
		expectedKeys = append(expectedKeys, key)
	}
	sort.Strings(expectedKeys)
	for _, key := range expectedKeys {
		if !existing["dkit-"+name+"_"+key] {
			fmt.Fprintf(os.Stderr, "Skipping absent Docker volume dkit-%s_%s; no volume was created.\n", name, key)
		}
	}

	var volumes []string
	for _, key := range []string{"mariadb_data", "postgres_data", "redis_data"} {
		if expected[key] && existing["dkit-"+name+"_"+key] {
			volumes = append(volumes, key)
		}
	}
	persistEntries := strings.Fields(app.Values["PERSIST"])
	for _, entry := range persistEntries {
		volumeName, _, _ := strings.Cut(entry, ":")
		key := "persist_" + volumeName
		if existing["dkit-"+name+"_"+key] {
			volumes = append(volumes, key)
		}
	}
	if len(volumes) == 0 {
		return "", nil
	}

	snapshotDir := filepath.Join(stateAppDir, "volume-snapshots")
	if err := os.Mkdir(snapshotDir, 0o700); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("temporary volume snapshot directory %s already exists; inspect and remove it before retrying", snapshotDir)
		}
		return "", fmt.Errorf("create temporary volume snapshot directory %s: %w", snapshotDir, err)
	}
	for _, key := range volumes {
		path := filepath.Join(snapshotDir, key+".tar.gz")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			cleanupErr := os.RemoveAll(snapshotDir)
			if cleanupErr != nil {
				return "", errors.Join(fmt.Errorf("create temporary archive for volume %s: %w", key, err), cleanupErr)
			}
			return "", fmt.Errorf("create temporary archive for volume %s: %w", key, err)
		}
		args := append(append([]string(nil), contextPrefix...), "run", "--rm", "--network", "none",
			"--mount", "type=volume,src=dkit-"+name+"_"+key+",dst=/data,readonly",
			"busybox:1.36.1", "tar", "-C", "/data", "-czf", "-", ".")
		cmd := exec.Command("docker", args...)
		cmd.Stdin, cmd.Stderr, cmd.Stdout = os.Stdin, os.Stderr, file
		runErr := cmd.Run()
		closeErr := file.Close()
		if runErr != nil || closeErr != nil {
			var operationErr error
			if runErr != nil {
				operationErr = fmt.Errorf("archive Docker volume dkit-%s_%s: %w", name, key, runErr)
			}
			if closeErr != nil {
				operationErr = errors.Join(operationErr, fmt.Errorf("close temporary volume archive %s: %w", path, closeErr))
			}
			if cleanupErr := os.RemoveAll(snapshotDir); cleanupErr != nil {
				operationErr = errors.Join(operationErr, fmt.Errorf("remove incomplete volume snapshots %s: %w", snapshotDir, cleanupErr))
			}
			return "", operationErr
		}
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			if err == nil {
				err = fmt.Errorf("archive is empty")
			}
			cleanupErr := os.RemoveAll(snapshotDir)
			return "", errors.Join(fmt.Errorf("verify temporary volume archive %s: %w", path, err), cleanupErr)
		}
	}
	return snapshotDir, nil
}

func backupHost(resticEnv []string) (string, error) {
	if configuredHost := environmentValue(resticEnv, "BACKUP_HOST"); configuredHost != "" {
		return configuredHost, nil
	}
	if configuredHost := os.Getenv("BACKUP_HOST"); configuredHost != "" {
		return configuredHost, nil
	}
	if configuredHost := environmentValue(resticEnv, "DKIT_HOSTNAME"); configuredHost != "" {
		return configuredHost, nil
	}
	if configuredHost := os.Getenv("DKIT_HOSTNAME"); configuredHost != "" {
		return configuredHost, nil
	}
	host, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("get backup host name: %w", err)
	}
	return host, nil
}

func dumpDatabase(name, database, dumpPath string, composeArgs []string) error {
	file, err := os.OpenFile(dumpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary %s dump: %w", database, err)
	}
	args := append(append([]string(nil), composeArgs...), "exec", "-T", database, "sh", "-c", dbDumpCommand(database))
	cmd := exec.Command("docker", args...)
	cmd.Stdin, cmd.Stderr, cmd.Stdout = os.Stdin, os.Stderr, file
	if err := cmd.Run(); err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("dump app %q database %s: %w", name, database, err), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary %s dump: %w", database, err)
	}
	return nil
}

func loadResticEnvironment(configDir string) ([]string, error) {
	path := filepath.Join(configDir, "backup.env")
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("missing %s; configure RESTIC_REPOSITORY and RESTIC_PASSWORD before using portable backups", path)
		}
		return nil, fmt.Errorf("inspect backup configuration %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("backup configuration %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("backup configuration %s must not be accessible to group or others (chmod 0600)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backup configuration %s: %w", path, err)
	}
	values, err := appconfig.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse backup configuration %s: %w", path, err)
	}
	if values["RESTIC_REPOSITORY"] == "" || values["RESTIC_PASSWORD"] == "" {
		return nil, fmt.Errorf("set RESTIC_REPOSITORY and RESTIC_PASSWORD in %s", path)
	}
	env := make([]string, 0, len(values))
	for _, key := range sortedKeys(values) {
		env = append(env, key+"="+values[key])
	}
	return env, nil
}

func runCommandWithEnv(name string, environment []string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = mergeEnvironment(os.Environ(), environment)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func mergeEnvironment(base, overrides []string) []string {
	merged := append([]string(nil), base...)
	for _, override := range overrides {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		prefix := key + "="
		filtered := merged[:0]
		for _, existing := range merged {
			if !strings.HasPrefix(existing, prefix) {
				filtered = append(filtered, existing)
			}
		}
		merged = append(filtered, override)
	}
	return merged
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func getApp(args []string, configDir string, output io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: dkit app get <name> [key|--json]")
	}
	app, err := appconfig.Read(configDir, args[0])
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", args[0], strings.Join(app.Errors, "; "))
	}
	if len(args) == 2 {
		if args[1] == "--json" {
			return json.NewEncoder(output).Encode(app.Values)
		}
		value, ok := app.Values[args[1]]
		if !ok {
			return fmt.Errorf("key %s is not set", args[1])
		}
		_, err := fmt.Fprintln(output, value)
		return err
	}
	keys := sortedKeys(app.Values)
	for _, key := range keys {
		if _, err := fmt.Fprintf(output, "%s=%s\n", key, configValue(app.Values[key])); err != nil {
			return err
		}
	}
	return nil
}

func setApp(args []string, configDir string, output io.Writer) error {
	set, unset, name, err := parseEditArgs(args)
	if err != nil {
		return fmt.Errorf("usage: dkit app set <name> KEY=VALUE... [--unset KEY]")
	}
	_, err = appconfig.Update(configDir, name, set, unset)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Updated app %q app.env. No Docker operations were performed.\n", name)
	return err
}

func envCommand(args []string, configDir string, output io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: dkit env list|set|unset <name> [options]")
	}
	action, name := args[0], args[1]
	switch action {
	case "list", "ls":
		if len(args) > 3 || (len(args) == 3 && args[2] != "--show" && args[2] != "--json") {
			return fmt.Errorf("usage: dkit env list <name> [--show|--json]")
		}
		values, err := appconfig.ReadEnv(configDir, name)
		if err != nil {
			return err
		}
		keys := sortedKeys(values)
		if len(args) == 3 && args[2] == "--json" {
			return json.NewEncoder(output).Encode(values)
		}
		show := len(args) == 3 && args[2] == "--show"
		for _, key := range keys {
			value := values[key]
			if !show && appconfig.IsSecretEnvKey(key) {
				value = "•••••• (--show)"
			}
			if _, err := fmt.Fprintf(output, "%s=%s\n", key, value); err != nil {
				return err
			}
		}
		return nil
	case "set":
		set, unset, _, err := parseEditArgs(append([]string{name}, args[2:]...))
		if err != nil || len(unset) > 0 || len(set) == 0 {
			return fmt.Errorf("usage: dkit env set <name> KEY=VALUE...")
		}
		if _, err := appconfig.UpdateEnv(configDir, name, set, nil); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Updated app %q env. No Docker operations were performed.\n", name)
		return err
	case "unset":
		if len(args) < 3 {
			return fmt.Errorf("usage: dkit env unset <name> <key...>")
		}
		unset := args[2:]
		if _, err := appconfig.UpdateEnv(configDir, name, nil, unset); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Removed environment keys from app %q. No Docker operations were performed.\n", name)
		return err
	default:
		return fmt.Errorf("unknown env command %q; use list, set, or unset", action)
	}
}

func parseEditArgs(args []string) (map[string]string, []string, string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return nil, nil, "", fmt.Errorf("app name is required")
	}
	name := args[0]
	set := make(map[string]string)
	var unset []string
	for i := 1; i < len(args); {
		if args[i] == "--unset" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return nil, nil, "", fmt.Errorf("--unset requires a key")
			}
			unset = append(unset, args[i+1])
			i += 2
			continue
		}
		key, value, ok := strings.Cut(args[i], "=")
		if !ok || key == "" {
			return nil, nil, "", fmt.Errorf("expected KEY=VALUE or --unset KEY")
		}
		set[key] = value
		i++
	}
	if len(set) == 0 && len(unset) == 0 {
		return nil, nil, "", fmt.Errorf("at least one setting is required")
	}
	return set, unset, name, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func configValue(value string) string {
	if strings.IndexFunc(value, func(r rune) bool { return r == ' ' || r == '\t' || r == '#' || r == '"' || r == '\'' }) >= 0 {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

func createApp(args []string, configDir string, output io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: dkit app create <name> --preset <preset> --domain <domain> [--source git|image] [--repo <url> | --image <image>] [--branch <branch>] [--services <list>] [--port <port>]")
	}
	options := appcreate.Options{Name: args[0]}
	for i := 1; i < len(args); {
		flag := args[i]
		i++
		switch flag {
		case "--yes", "-y":
			continue
		case "--preset", "-p", "--source", "--domain", "-d", "--repo", "-r", "--branch", "-b",
			"--image", "-i", "--services", "-s", "--port":
			if i >= len(args) || strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("option %s requires a value", flag)
			}
			value := args[i]
			i++
			switch flag {
			case "--preset", "-p":
				options.Preset = value
			case "--source":
				options.Source = value
			case "--domain", "-d":
				options.Domains = append(options.Domains, value)
			case "--repo", "-r":
				options.Repo = value
			case "--branch", "-b":
				options.Branch = value
			case "--image", "-i":
				options.Image = value
			case "--services", "-s":
				options.Services = value
			case "--port":
				options.Port = value
			}
		default:
			return fmt.Errorf("unknown app create option %q", flag)
		}
	}
	appDir, err := appcreate.Create(configDir, options)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "App %q created from preset %q.\nConfig: %s\nNo Docker operations were performed.\n",
		options.Name, options.Preset, appDir)
	return err
}

func presetCommand(args []string, configDir string, output io.Writer) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] != "list" && args[0] != "ls" {
		return runLegacy(append([]string{"preset"}, args...), portable.Paths{Config: configDir})
	}
	if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return fmt.Errorf("usage: dkit preset list [--json]")
	}
	presets, err := presetcatalog.List(configDir)
	if err != nil {
		return err
	}
	if len(args) == 2 {
		return json.NewEncoder(output).Encode(presets)
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "PRESET\tDESCRIPTION"); err != nil {
		return err
	}
	for _, preset := range presets {
		name := preset.Name
		if preset.Local {
			name += " [custom]"
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\n", name, truncateDescription(preset.Description, 90)); err != nil {
			return err
		}
	}
	return table.Flush()
}

func truncateDescription(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func listApps(args []string, configDir string, output io.Writer) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
		return fmt.Errorf("usage: dkit app list [--json]")
	}
	apps, err := appconfig.List(configDir)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		return json.NewEncoder(output).Encode(apps)
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "APP\tDOMAINS\tSOURCE\tBUILD\tCONFIG"); err != nil {
		return err
	}
	for _, app := range apps {
		result := "valid"
		if !app.Valid {
			result = "invalid: " + strings.Join(app.Errors, "; ")
		}
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			app.Name, strings.Join(app.Domains, ","), app.Source, app.Build, result); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Validation scope: %s\n", appConfigValidationScope)
	return err
}

func checkApp(args []string, configDir string, output io.Writer) error {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
		return fmt.Errorf("usage: dkit app check <name> [--json]")
	}
	name := args[0]
	app, err := appconfig.Read(configDir, name)
	result := appCheckResult{Name: name, Valid: err == nil && app.Valid, Scope: appConfigValidationScope}
	if err != nil {
		result.Errors = []string{err.Error()}
	} else {
		result.Errors = app.Errors
	}
	if len(args) == 2 {
		if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
			return encodeErr
		}
	} else if err != nil {
		if _, writeErr := fmt.Fprintf(output, "App %q config could not be checked: %s\n", name, err); writeErr != nil {
			return writeErr
		}
	} else if app.Valid {
		if _, writeErr := fmt.Fprintf(output, "App %q app.env is valid.\nValidation scope: %s\n", name, appConfigValidationScope); writeErr != nil {
			return writeErr
		}
	} else {
		if _, writeErr := fmt.Fprintf(output, "App %q app.env is invalid:\n", name); writeErr != nil {
			return writeErr
		}
		for _, issue := range app.Errors {
			if _, writeErr := fmt.Fprintf(output, "  - %s\n", issue); writeErr != nil {
				return writeErr
			}
		}
		if _, writeErr := fmt.Fprintf(output, "Validation scope: %s\n", appConfigValidationScope); writeErr != nil {
			return writeErr
		}
	}
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid", name)
	}
	return nil
}

func collectStatus(context string, output func(args ...string) ([]byte, error)) (daemonStatus, error) {
	infoArgs := append(contextArgs(context), "info", "--format", "{{json .}}")
	infoBytes, err := output(infoArgs...)
	if err != nil {
		return daemonStatus{}, fmt.Errorf("read Docker daemon info: %w", err)
	}
	var info dockerInfo
	if err := json.Unmarshal(infoBytes, &info); err != nil {
		return daemonStatus{}, fmt.Errorf("parse Docker daemon info: %w", err)
	}
	status := daemonStatus{
		Name: info.Name, ServerVersion: info.ServerVersion, OperatingSystem: info.OperatingSystem,
		OSType: info.OSType, Architecture: info.Architecture, Containers: info.Containers,
		ContainersRunning: info.ContainersRunning, ContainersPaused: info.ContainersPaused,
		ContainersStopped: info.ContainersStopped,
	}

	containersArgs := append(contextArgs(context), "ps", "-a", "--format", "{{json .}}")
	containerBytes, err := output(containersArgs...)
	if err != nil {
		return daemonStatus{}, fmt.Errorf("list Docker containers: %w", err)
	}
	status.ContainerList, err = parseContainerStatuses(containerBytes)
	if err != nil {
		return daemonStatus{}, err
	}
	return status, nil
}

func parseContainerStatuses(data []byte) ([]containerStatus, error) {
	var containers []containerStatus
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var container containerStatus
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			return nil, fmt.Errorf("parse Docker container list line %d: %w", lineNumber+1, err)
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func printStatus(w io.Writer, status daemonStatus) error {
	if _, err := fmt.Fprintf(w, "Docker context: %s\n", status.Context); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Daemon:         %s\n", defaultText(status.Name, "(unknown)")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Server version: %s\n", defaultText(status.ServerVersion, "(unknown)")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Operating system: %s (%s/%s)\n", defaultText(status.OperatingSystem, "(unknown)"),
		defaultText(status.OSType, "(unknown)"), defaultText(status.Architecture, "(unknown)")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Containers: %d total, %d running, %d paused, %d stopped\n",
		status.Containers, status.ContainersRunning, status.ContainersPaused, status.ContainersStopped); err != nil {
		return err
	}
	if len(status.ContainerList) == 0 {
		_, err := fmt.Fprintln(w, "No containers.")
		return err
	}
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "\nID\tNAME\tIMAGE\tSTATE\tSTATUS"); err != nil {
		return err
	}
	for _, container := range status.ContainerList {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			container.ID, container.Names, container.Image, container.State, container.Status); err != nil {
			return err
		}
	}
	return table.Flush()
}

func doctor(paths portable.Paths) error {
	context, err := selectedContext(paths)
	if err != nil {
		return err
	}
	fmt.Printf("Docker context: %s\n", defaultText(context, "(Docker default)"))
	for _, check := range [][]string{{"version"}, {"compose", "version"}, {"info"}} {
		if _, err := dockerOutput(contextArgs(context), check...); err != nil {
			return fmt.Errorf("docker %s: %w", strings.Join(check, " "), err)
		}
		fmt.Printf("OK: docker %s\n", strings.Join(check, " "))
	}
	return nil
}

func defaultText(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func contextArgs(context string) []string {
	if context == "" {
		return nil
	}
	return []string{"--context", context}
}

func dockerOutput(prefix []string, args ...string) ([]byte, error) {
	all := append(append([]string{}, prefix...), args...)
	cmd := exec.Command("docker", all...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(all, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func runLegacy(args []string, paths portable.Paths) error {
	if runtime.GOOS != "linux" {
		return errors.New("deployment commands still require the Bash/Linux engine and are not yet supported on Windows or macOS; portable paths and Docker-context commands are available")
	}
	root := findRoot()
	script := filepath.Join(root, "bin", "dkit")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("legacy deployment engine not found at %s: %w", script, err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return fmt.Errorf("the deployment engine requires Bash: %w", err)
	}
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	cmd.Env = os.Environ()
	cmd.Env = setEnv(cmd.Env, "DKIT_ROOT", root)
	if configDir := os.Getenv("DKIT_CONFIG_DIR"); configDir != "" {
		cmd.Env = setEnv(cmd.Env, "DKIT_ETC", configDir)
	}
	if stateDir := os.Getenv("DKIT_STATE_DIR"); stateDir != "" {
		cmd.Env = setEnv(cmd.Env, "DKIT_STATE", stateDir)
	}
	if context, err := selectedContext(paths); err != nil {
		return err
	} else if context != "" {
		cmd.Env = setEnv(cmd.Env, "DOCKER_CONTEXT", context)
	}
	return cmd.Run()
}

func selectedContext(paths portable.Paths) (string, error) {
	cfg, err := loadSettings(paths.Config)
	if err != nil {
		return "", err
	}
	return resolveContext(os.Getenv("DOCKER_CONTEXT"), cfg.DockerContext, func() (string, error) {
		out, err := dockerOutput(nil, "context", "show")
		if err != nil {
			return "", fmt.Errorf("resolve active Docker context: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	})
}

func resolveContext(environment, saved string, dockerCurrent func() (string, error)) (string, error) {
	context := portable.ContextFrom(environment, saved, "")
	if context != "" {
		return context, nil
	}
	context, err := dockerCurrent()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(context) == "" {
		return "", errors.New("Docker CLI returned an empty active context")
	}
	return strings.TrimSpace(context), nil
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func findRoot() string {
	if root := os.Getenv("DKIT_ROOT"); root != "" {
		return root
	}
	exe, _ := os.Executable()
	for _, start := range []string{filepath.Dir(exe), "."} {
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			if _, err := os.Stat(filepath.Join(dir, "bin", "dkit")); err == nil {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	if _, err := os.Stat(filepath.Join("/opt/dkit", "bin", "dkit")); err == nil {
		return "/opt/dkit"
	}
	return "."
}

func loadSettings(configDir string) (settings, error) {
	var cfg settings
	data, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("read dkit configuration: %w", err)
	}
	return cfg, nil
}

func saveSettings(configDir string, cfg settings) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(configDir, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(configDir, "config.json"))
}
