package appconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	keyPattern        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	appNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	domainPattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	buildPattern      = regexp.MustCompile(`^[a-z0-9_-]+$`)
	healthPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~/?=&-]*$`)
	workerPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	workerPattern     = regexp.MustCompile(`^[a-z][a-z0-9]{0,20}$`)
	userPattern       = regexp.MustCompile(`^[a-z0-9_-]+(:[a-z0-9_-]+)?$`)
	persistPattern    = regexp.MustCompile(`^[a-z0-9_-]+:/[^:]*$`)
	ownerPattern      = regexp.MustCompile(`^[0-9]+(:[0-9]+)?$`)
	dbPattern         = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	bucketPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{2,62}$`)
	memoryPattern     = regexp.MustCompile(`^[0-9]+[kmgKMG]?$`)
)

var knownServices = map[string]bool{
	"mariadb": true, "postgres": true, "redis": true, "s3": true,
}

var shareableServices = map[string]bool{
	"mariadb": true, "postgres": true, "s3": true,
}

type App struct {
	Name     string            `json:"name"`
	Domains  []string          `json:"domains"`
	Source   string            `json:"source"`
	Build    string            `json:"build"`
	Services []string          `json:"services"`
	Values   map[string]string `json:"-"`
	Errors   []string          `json:"errors"`
	Valid    bool              `json:"valid"`
}

func ValidName(name string) bool {
	return appNamePattern.MatchString(name)
}

// Parse reads the app.env KEY=VALUE format without evaluating shell code.
// Duplicate keys use the last value, as in the legacy Bash cfg_load helper.
func Parse(data []byte) (map[string]string, error) {
	values := make(map[string]string)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		line = strings.TrimLeft(line, " \t")
		if strings.HasPrefix(line, "export") && len(line) > len("export") && (line[len("export")] == ' ' || line[len("export")] == '\t') {
			line = strings.TrimLeft(line[len("export"):], " \t")
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", i+1)
		}
		value = parseValue(value)
		values[key] = value
	}
	return values, nil
}

func parseValue(value string) string {
	if strings.HasPrefix(value, "\"") {
		if parsed, ok := unwrapQuoted(value, '"'); ok {
			return strings.ReplaceAll(parsed, `\"`, `"`)
		}
	}
	if strings.HasPrefix(value, "'") {
		if parsed, ok := unwrapQuoted(value, '\''); ok {
			return parsed
		}
	}
	if comment := commentIndex(value); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimRight(value, " \t")
}

func unwrapQuoted(value string, quote byte) (string, bool) {
	if len(value) < 2 || value[0] != quote {
		return "", false
	}
	end := strings.LastIndexByte(value, quote)
	if end == 0 || strings.TrimSpace(value[end+1:]) != "" {
		return "", false
	}
	return value[1:end], true
}

func commentIndex(value string) int {
	for i := 1; i < len(value); i++ {
		if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
			return i
		}
	}
	return -1
}

func Validate(name string, values map[string]string) App {
	app := App{
		Name:   name,
		Values: values,
		Source: valueOr(values, "SOURCE", "git"),
		Build:  valueOr(values, "BUILD", "dockerfile"),
		Valid:  true,
	}
	addError := func(format string, args ...any) {
		app.Errors = append(app.Errors, fmt.Sprintf(format, args...))
	}
	if !ValidName(name) {
		addError("invalid app name %q (expected lowercase letters, digits, and hyphens; max 31 characters)", name)
	}

	app.Domains = splitList(values["DOMAINS"])
	if len(app.Domains) == 0 && values["PUBLIC"] != "0" {
		addError("set DOMAINS or set PUBLIC=0")
	}
	for _, domain := range app.Domains {
		if !domainPattern.MatchString(domain) {
			addError("invalid domain %q", domain)
		}
	}

	switch app.Source {
	case "git":
		if values["GIT_REPOSITORY"] == "" {
			addError("SOURCE=git requires GIT_REPOSITORY")
		}
	case "image":
		if values["IMAGE"] == "" {
			addError("SOURCE=image requires IMAGE")
		}
	default:
		addError("SOURCE must be git or image")
	}
	if !buildPattern.MatchString(app.Build) {
		addError("invalid BUILD %q", app.Build)
	}
	if port := valueOr(values, "WEB_PORT", "8080"); !isDecimal(port, 2, 5) {
		addError("WEB_PORT must contain 2 to 5 digits")
	}
	if port := values["PUBLISH_PORT"]; port != "" {
		parsed, err := strconv.Atoi(port)
		if err != nil || parsed < 1 || parsed > 65535 {
			addError("PUBLISH_PORT must be an integer from 1 to 65535")
		}
	}
	if path := valueOr(values, "HEALTH_PATH", "/"); !healthPathPattern.MatchString(path) {
		addError("invalid HEALTH_PATH %q", path)
	}

	app.Services = splitList(values["SERVICES"])
	for _, service := range app.Services {
		if !knownServices[service] {
			addError("unknown service %q (available: mariadb, postgres, redis, s3)", service)
		}
	}

	workers := splitList(values["WORKERS"])
	for _, worker := range workers {
		if !workerPattern.MatchString(worker) {
			addError("invalid worker name %q", worker)
			continue
		}
		if worker == "web" || knownServices[worker] {
			addError("worker name %q is reserved", worker)
		}
		if values["WORKER_"+worker+"_CMD"] == "" {
			addError("WORKERS includes %q but WORKER_%s_CMD is empty", worker, worker)
		}
		if port := values["WORKER_"+worker+"_PORT"]; port != "" && !isDecimal(port, 2, 5) {
			addError("invalid WORKER_%s_PORT %q", worker, port)
		}
		path := values["WORKER_"+worker+"_PATH"]
		if path != "" && !workerPathPattern.MatchString(path) {
			addError("invalid WORKER_%s_PATH %q", worker, path)
		}
		if path != "" && values["WORKER_"+worker+"_PORT"] == "" {
			addError("WORKER_%s_PATH requires WORKER_%s_PORT", worker, worker)
		}
		if user := values["WORKER_"+worker+"_USER"]; user != "" && !userPattern.MatchString(user) {
			addError("invalid WORKER_%s_USER %q", worker, user)
		}
	}

	persistNames, persistPaths := map[string]bool{}, map[string]bool{}
	for _, persist := range strings.Fields(values["PERSIST"]) {
		if !persistPattern.MatchString(persist) {
			addError("invalid PERSIST entry %q (expected name:/container/path)", persist)
			continue
		}
		name, path, _ := strings.Cut(persist, ":")
		if persistNames[name] {
			addError("duplicate PERSIST volume name %q", name)
		}
		if persistPaths[path] {
			addError("duplicate PERSIST container path %q", path)
		}
		persistNames[name], persistPaths[path] = true, true
	}
	if user := values["MIGRATE_USER"]; user != "" && !userPattern.MatchString(user) {
		addError("invalid MIGRATE_USER %q", user)
	}
	if owner := values["PERSIST_OWNER"]; owner != "" && !ownerPattern.MatchString(owner) {
		addError("PERSIST_OWNER must be uid or uid:gid")
	}

	shared := splitList(values["SHARED_SERVICES"])
	services := make(map[string]bool, len(app.Services))
	for _, service := range app.Services {
		services[service] = true
	}
	for _, service := range shared {
		if !shareableServices[service] {
			addError("service %q cannot be shared (shareable: mariadb, postgres, s3)", service)
		}
	}
	for _, key := range []string{"DB_NAME", "DB_USER"} {
		if value := values[key]; value != "" && !dbPattern.MatchString(value) {
			addError("%s must start with a lowercase letter and contain only lowercase letters, digits, and underscores", key)
		}
	}
	if bucket := values["S3_BUCKET"]; bucket != "" && !bucketPattern.MatchString(bucket) {
		addError("invalid S3_BUCKET %q", bucket)
	}
	if domain := values["S3_DOMAIN"]; domain != "" {
		if !domainPattern.MatchString(domain) {
			addError("invalid S3_DOMAIN %q", domain)
		}
		if !services["s3"] {
			addError("S3_DOMAIN requires service s3")
		}
	}
	if limit := values["MEMORY_LIMIT"]; limit != "" && !memoryPattern.MatchString(limit) {
		addError("invalid MEMORY_LIMIT %q (example: 512m or 2g)", limit)
	}
	app.Valid = len(app.Errors) == 0
	return app
}

func valueOr(values map[string]string, key, fallback string) string {
	if value := values[key]; value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	fields := strings.Fields(strings.ReplaceAll(value, ",", " "))
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func isDecimal(value string, min, max int) bool {
	if len(value) < min || len(value) > max {
		return false
	}
	_, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func AppFilePath(configDir, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid app name %q", name)
	}
	return filepath.Join(configDir, "apps", name, "app.env"), nil
}

func Read(configDir, name string) (App, error) {
	path, err := AppFilePath(configDir, name)
	if err != nil {
		return App{}, err
	}
	appDir := filepath.Dir(path)
	info, err := os.Lstat(appDir)
	if err != nil {
		return App{}, fmt.Errorf("inspect app %q directory %s: %w", name, appDir, err)
	}
	if !info.IsDir() {
		return App{}, fmt.Errorf("app %q path %s is not a directory", name, appDir)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return App{}, fmt.Errorf("inspect app %q config %s: %w", name, path, err)
	}
	if !info.Mode().IsRegular() {
		return App{}, fmt.Errorf("app %q config %s is not a regular file", name, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return App{}, fmt.Errorf("read app %q config %s: %w", name, path, err)
	}
	values, err := Parse(data)
	if err != nil {
		return App{}, fmt.Errorf("parse app %q config: %w", name, err)
	}
	return Validate(name, values), nil
}

func List(configDir string) ([]App, error) {
	appsDir := filepath.Join(configDir, "apps")
	entries, err := os.ReadDir(appsDir)
	if os.IsNotExist(err) {
		return []App{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read app directory %s: %w", appsDir, err)
	}
	apps := make([]App, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appEnvPath := filepath.Join(appsDir, entry.Name(), "app.env")
		info, err := os.Lstat(appEnvPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect app config %s: %w", appEnvPath, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		app, err := Read(configDir, entry.Name())
		if err != nil {
			apps = append(apps, App{
				Name: entry.Name(), Valid: false, Errors: []string{err.Error()},
			})
			continue
		}
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}
