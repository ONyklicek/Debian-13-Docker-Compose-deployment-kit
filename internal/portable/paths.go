package portable

import (
	"os"
	"path/filepath"
	"runtime"
)

type Paths struct {
	Config string `json:"config"`
	State  string `json:"state"`
}

// ResolvePaths returns per-user paths and preserves overrides used by the
// existing Linux deployment engine.
func ResolvePaths() (Paths, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	return pathsFor(runtime.GOOS, home, configHome, cacheHome, os.Getenv), nil
}

func pathsFor(goos, home, configHome, cacheHome string, getenv func(string) string) Paths {
	config := first(getenv("DKIT_CONFIG_DIR"), getenv("DKIT_ETC"), filepath.Join(configHome, "dkit"))
	stateDefault := filepath.Join(home, ".local", "share", "dkit")
	switch goos {
	case "windows":
		stateDefault = filepath.Join(first(getenv("LOCALAPPDATA"), cacheHome), "dkit")
	case "darwin":
		stateDefault = filepath.Join(home, "Library", "Application Support", "dkit")
	default:
		dataHome := getenv("XDG_DATA_HOME")
		if dataHome != "" && filepath.IsAbs(dataHome) {
			stateDefault = filepath.Join(dataHome, "dkit")
		}
	}
	state := first(getenv("DKIT_STATE_DIR"), getenv("DKIT_STATE"), stateDefault)
	return Paths{Config: filepath.Clean(config), State: filepath.Clean(state)}
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
