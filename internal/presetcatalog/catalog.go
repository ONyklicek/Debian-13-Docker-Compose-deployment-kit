package presetcatalog

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	builtin "github.com/onyklicek/debian-13-docker-compose-deployment-kit/presets"
)

type Template struct {
	AppEnv []byte
	Env    []byte
}

type Preset struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Local       bool   `json:"local"`
}

var presetNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

func Read(configDir, name string) (Template, error) {
	if !presetNamePattern.MatchString(name) {
		return Template{}, fmt.Errorf("invalid preset name %q", name)
	}
	localDir := filepath.Join(configDir, "presets", name)
	localAppEnv := filepath.Join(localDir, "app.env")
	data, err := os.ReadFile(localAppEnv)
	if err == nil {
		template := Template{AppEnv: data}
		if env, envErr := os.ReadFile(filepath.Join(localDir, "env")); envErr == nil {
			template.Env = env
		} else if !os.IsNotExist(envErr) {
			return Template{}, fmt.Errorf("read local preset env %s: %w", filepath.Join(localDir, "env"), envErr)
		}
		return template, nil
	}
	if !os.IsNotExist(err) {
		return Template{}, fmt.Errorf("read local preset %s: %w", localAppEnv, err)
	}
	appEnv, err := fs.ReadFile(builtin.Files, path.Join(name, "app.env"))
	if err != nil {
		return Template{}, fmt.Errorf("read built-in preset %q: %w", name, err)
	}

	template := Template{AppEnv: appEnv}
	if env, err := fs.ReadFile(builtin.Files, path.Join(name, "env")); err == nil {
		template.Env = env
	} else if !os.IsNotExist(err) {
		return Template{}, fmt.Errorf("read built-in preset %q env: %w", name, err)
	}
	return template, nil
}

func List(configDir string) ([]Preset, error) {
	presets := make(map[string]Preset)
	builtinPaths, err := fs.Glob(builtin.Files, "*/app.env")
	if err != nil {
		return nil, fmt.Errorf("list built-in presets: %w", err)
	}
	for _, file := range builtinPaths {
		data, err := fs.ReadFile(builtin.Files, file)
		if err != nil {
			return nil, fmt.Errorf("read built-in preset %s: %w", file, err)
		}
		name := path.Dir(file)
		presets[name] = Preset{Name: name, Description: description(data)}
	}

	localDir := filepath.Join(configDir, "presets")
	entries, err := os.ReadDir(localDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read local preset directory %s: %w", localDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		file := filepath.Join(localDir, entry.Name(), "app.env")
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect local preset %s: %w", file, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read local preset %s: %w", file, err)
		}
		name := entry.Name()
		presets[name] = Preset{Name: name, Description: description(data), Local: true}
	}

	result := make([]Preset, 0, len(presets))
	for _, preset := range presets {
		result = append(result, preset)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func description(data []byte) string {
	firstLine, _, _ := strings.Cut(string(data), "\n")
	firstLine = strings.TrimSuffix(firstLine, "\r")
	if !strings.HasPrefix(firstLine, "#") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(firstLine, "#"))
}
