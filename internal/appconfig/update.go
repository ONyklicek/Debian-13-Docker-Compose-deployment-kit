package appconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	envSecretName     = regexp.MustCompile(`(?i)(KEY|SECRET|PASSWORD|TOKEN|PASS)$`)
	safeUnquotedValue = regexp.MustCompile(`^[A-Za-z0-9_./:@,+=%-]*$`)
)

func Update(configDir, name string, set map[string]string, unset []string) (App, error) {
	path, err := AppFilePath(configDir, name)
	if err != nil {
		return App{}, err
	}
	if err := requireValidApp(configDir, name); err != nil {
		return App{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return App{}, fmt.Errorf("read app %q config %s: %w", name, path, err)
	}
	updated, err := mutate(data, set, unset, false)
	if err != nil {
		return App{}, err
	}
	values, err := Parse(updated)
	if err != nil {
		return App{}, err
	}
	app := Validate(name, values)
	if !app.Valid {
		return app, fmt.Errorf("updated app configuration is invalid: %s", strings.Join(app.Errors, "; "))
	}
	if err := atomicReplace(path, updated, 0o640); err != nil {
		return App{}, err
	}
	return app, nil
}

func Rewrite(data []byte, set map[string]string, unset []string) ([]byte, error) {
	return mutate(data, set, unset, false)
}

func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	return atomicReplace(path, data, mode)
}

func ReadEnv(configDir, name string) (map[string]string, error) {
	path, err := AppFilePath(configDir, name)
	if err != nil {
		return nil, err
	}
	if err := requireValidApp(configDir, name); err != nil {
		return nil, err
	}
	envPath := filepath.Join(filepath.Dir(path), "env")
	data, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read app %q environment %s: %w", name, envPath, err)
	}
	return ParseEnv(data)
}

func UpdateEnv(configDir, name string, set map[string]string, unset []string) (map[string]string, error) {
	path, err := AppFilePath(configDir, name)
	if err != nil {
		return nil, err
	}
	if err := requireValidApp(configDir, name); err != nil {
		return nil, err
	}
	envPath := filepath.Join(filepath.Dir(path), "env")
	data, err := os.ReadFile(envPath)
	if os.IsNotExist(err) {
		data = nil
	} else if err != nil {
		return nil, fmt.Errorf("read app %q environment %s: %w", name, envPath, err)
	}
	updated, err := mutate(data, set, unset, true)
	if err != nil {
		return nil, err
	}
	values, err := ParseEnv(updated)
	if err != nil {
		return nil, err
	}
	if err := atomicReplace(envPath, updated, 0o600); err != nil {
		return nil, err
	}
	return values, nil
}

func requireValidApp(configDir, name string) error {
	app, err := Read(configDir, name)
	if err != nil {
		return err
	}
	if !app.Valid {
		return fmt.Errorf("app %q configuration is invalid: %s", name, strings.Join(app.Errors, "; "))
	}
	return nil
}

func ParseEnv(data []byte) (map[string]string, error) {
	values, err := parse(data, true)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func IsSecretEnvKey(key string) bool {
	return envSecretName.MatchString(key)
}

func parse(data []byte, envMode bool) (map[string]string, error) {
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
		rawValue := value
		value = parseValue(value)
		if envMode && strings.HasPrefix(strings.TrimLeft(rawValue, " \t"), `"`) {
			value = strings.ReplaceAll(value, "$$", "$")
			value = strings.ReplaceAll(value, `\\`, `\`)
		}
		values[key] = value
	}
	return values, nil
}

func mutate(data []byte, set map[string]string, unset []string, envMode bool) ([]byte, error) {
	normalized := make(map[string]string, len(set))
	for key, value := range set {
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid configuration key %q", key)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("value for %s contains a newline or NUL byte", key)
		}
		normalized[key] = value
	}
	unsetKeys := make(map[string]bool, len(unset))
	for _, key := range unset {
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid configuration key %q", key)
		}
		unsetKeys[key] = true
		delete(normalized, key)
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	written := make(map[string]bool, len(normalized))
	out := make([]string, 0, len(lines)+len(normalized))
	for _, line := range lines {
		key, ok := assignmentKey(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if unsetKeys[key] {
			continue
		}
		if value, exists := normalized[key]; exists {
			if !written[key] {
				out = append(out, key+"="+quoteValue(value, envMode))
				written[key] = true
			}
			continue
		}
		out = append(out, line)
	}
	keys := make([]string, 0, len(normalized))
	for key := range normalized {
		if !written[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out = append(out, key+"="+quoteValue(normalized[key], envMode))
	}
	if len(out) == 0 {
		return []byte{}, nil
	}
	return []byte(strings.Join(out, "\n") + "\n"), nil
}

func assignmentKey(line string) (string, bool) {
	line = strings.TrimLeft(line, " \t")
	if strings.HasPrefix(line, "export") && len(line) > len("export") && (line[len("export")] == ' ' || line[len("export")] == '\t') {
		line = strings.TrimLeft(line[len("export"):], " \t")
	}
	key, _, ok := strings.Cut(line, "=")
	return key, ok && keyPattern.MatchString(key)
}

func quoteValue(value string, envMode bool) string {
	if safeUnquotedValue.MatchString(value) {
		return value
	}
	if !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	if envMode {
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, "$", "$$")
	}
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func atomicReplace(path string, data []byte, defaultMode os.FileMode) error {
	mode := defaultMode
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular file %s", path)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".dkit-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config in %s: %w", dir, err)
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write temporary config %s: %w", tmp, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary config %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("set permissions on temporary config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("atomically replace %s: %w", path, err)
	}
	return nil
}
