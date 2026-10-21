package appcreate

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/presetcatalog"
)

type Options struct {
	Name     string
	Preset   string
	Source   string
	Domains  []string
	Repo     string
	Branch   string
	Image    string
	Services string
	Port     string
}

var safeSubstitution = regexp.MustCompile(`^[^\r\n\x00"'#\\]*$`)

func Create(configDir string, options Options) (createdDir string, retErr error) {
	return create(configDir, options, writeFile)
}

func create(configDir string, options Options, write func(string, []byte, os.FileMode) error) (createdDir string, retErr error) {
	if !appconfig.ValidName(options.Name) {
		return "", fmt.Errorf("invalid app name %q", options.Name)
	}
	if options.Preset == "" {
		return "", fmt.Errorf("a preset is required")
	}
	var domainFields []string
	for _, domain := range options.Domains {
		domainFields = append(domainFields, strings.Fields(strings.ReplaceAll(domain, ",", " "))...)
	}
	domains := strings.Join(domainFields, ",")
	if strings.TrimSpace(domains) == "" {
		return "", fmt.Errorf("at least one --domain is required")
	}
	for _, field := range []struct{ name, value string }{
		{"domain", domains}, {"repository", options.Repo}, {"branch", options.Branch}, {"image", options.Image},
	} {
		if !safeSubstitution.MatchString(field.value) {
			return "", fmt.Errorf("%s contains characters that cannot safely be written to app.env", field.name)
		}
	}

	template, err := presetcatalog.Read(configDir, options.Preset)
	if err != nil {
		return "", err
	}
	values, err := appconfig.Parse(template.AppEnv)
	if err != nil {
		return "", fmt.Errorf("parse preset %q app.env: %w", options.Preset, err)
	}
	source := values["SOURCE"]
	if source == "" {
		source = "git"
	}
	if options.Source != "" && options.Source != source {
		return "", fmt.Errorf("--source %q does not match preset %q (SOURCE=%s)", options.Source, options.Preset, source)
	}
	if source == "image" && options.Image == "" {
		return "", fmt.Errorf("preset %q requires --image", options.Preset)
	}
	if source == "git" && options.Repo == "" {
		return "", fmt.Errorf("preset %q requires --repo", options.Preset)
	}
	if source != "git" && source != "image" {
		return "", fmt.Errorf("preset %q has unsupported SOURCE %q", options.Preset, source)
	}
	if options.Branch == "" {
		options.Branch = "main"
	}
	if !safeSubstitution.MatchString(options.Branch) {
		return "", fmt.Errorf("branch contains characters that cannot safely be written to app.env")
	}

	appEnv := strings.NewReplacer(
		"__APP__", options.Name,
		"__DOMAINS__", domains,
		"__DOMAIN__", strings.Split(domains, ",")[0],
		"__REPO__", options.Repo,
		"__BRANCH__", options.Branch,
		"__IMAGE__", options.Image,
	).Replace(string(template.AppEnv))
	if options.Services != "" && options.Services != "-" {
		if !safeSubstitution.MatchString(options.Services) {
			return "", fmt.Errorf("services contain characters that cannot safely be written to app.env")
		}
		appEnv += "\nSERVICES=" + quoteConfigValue(strings.ReplaceAll(options.Services, ",", " ")) + "\n"
	}
	if options.Port != "" {
		if !safeSubstitution.MatchString(options.Port) {
			return "", fmt.Errorf("port contains characters that cannot safely be written to app.env")
		}
		appEnv += "\nWEB_PORT=" + options.Port + "\n"
	}
	parsed, err := appconfig.Parse([]byte(appEnv))
	if err != nil {
		return "", fmt.Errorf("render preset %q: %w", options.Preset, err)
	}
	app := appconfig.Validate(options.Name, parsed)
	if !app.Valid {
		return "", fmt.Errorf("rendered app configuration is invalid: %s", strings.Join(app.Errors, "; "))
	}

	env, err := expandEnvironment(string(template.Env), options.Name, strings.Split(domains, ",")[0])
	if err != nil {
		return "", err
	}
	appsDir := filepath.Join(configDir, "apps")
	if err := os.MkdirAll(appsDir, 0o700); err != nil {
		return "", fmt.Errorf("create apps directory: %w", err)
	}
	createdDir = filepath.Join(appsDir, options.Name)
	if _, err := os.Lstat(createdDir); err == nil {
		return "", fmt.Errorf("app %q already exists", options.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect app path %s: %w", createdDir, err)
	}
	stageDir, err := os.MkdirTemp(appsDir, ".dkit-create-*")
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
	if err := write(filepath.Join(stageDir, "env"), []byte(env), 0o600); err != nil {
		return "", err
	}
	if err := write(filepath.Join(stageDir, "app.env"), []byte(appEnv), 0o640); err != nil {
		return "", err
	}
	if _, err := os.Lstat(createdDir); err == nil {
		return "", fmt.Errorf("app %q already exists", options.Name)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect app path %s: %w", createdDir, err)
	}
	if err := os.Rename(stageDir, createdDir); err != nil {
		if _, statErr := os.Lstat(createdDir); statErr == nil {
			return "", fmt.Errorf("app %q already exists", options.Name)
		}
		return "", fmt.Errorf("publish app config atomically: %w", err)
	}
	failed = false
	return createdDir, nil
}

func expandEnvironment(input, appName, domain string) (string, error) {
	for _, size := range []int{32, 16} {
		var marker string
		if size == 32 {
			marker = "__HEX32__"
		} else {
			marker = "__HEX16__"
		}
		for strings.Contains(input, marker) {
			randomBytes := make([]byte, size)
			if _, err := rand.Read(randomBytes); err != nil {
				return "", fmt.Errorf("generate preset secret: %w", err)
			}
			input = strings.Replace(input, marker, hex.EncodeToString(randomBytes), 1)
		}
	}
	for strings.Contains(input, "__LARAVEL_KEY__") {
		randomBytes := make([]byte, 32)
		if _, err := rand.Read(randomBytes); err != nil {
			return "", fmt.Errorf("generate Laravel app key: %w", err)
		}
		value := "base64:" + base64.StdEncoding.EncodeToString(randomBytes)
		input = strings.Replace(input, "__LARAVEL_KEY__", value, 1)
	}
	return strings.NewReplacer("__APP__", appName, "__DOMAIN__", domain).Replace(input), nil
}

func quoteConfigValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
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
