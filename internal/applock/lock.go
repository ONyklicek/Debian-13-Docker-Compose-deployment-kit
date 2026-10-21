package applock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

func Acquire(stateDir, name string) (*flock.Flock, error) {
	if stateDir == "" || !appconfig.ValidName(name) {
		return nil, fmt.Errorf("invalid app lock location")
	}
	dir := filepath.Join(stateDir, "apps", ".locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create app lock directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect app lock directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("app lock directory is not a real directory: %s", dir)
	}
	path := filepath.Join(dir, name+".lock")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("app lock file is not regular: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect app lock file: %w", err)
	}
	lock := flock.New(path)
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("lock app %q lifecycle: %w", name, err)
	}
	return lock, nil
}

func Release(name string, lock *flock.Flock) error {
	if lock == nil {
		return nil
	}
	if err := lock.Unlock(); err != nil {
		return fmt.Errorf("unlock app %q lifecycle: %w", name, err)
	}
	return nil
}
