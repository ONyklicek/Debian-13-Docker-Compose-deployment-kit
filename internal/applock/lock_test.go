package applock

import (
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
)

func TestAcquireExcludesConcurrentLifecycleForSameApp(t *testing.T) {
	stateDir := t.TempDir()
	lock, err := Acquire(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := Release("demo", lock); err != nil {
			t.Error(err)
		}
	}()

	contender := flock.New(filepath.Join(stateDir, "apps", ".locks", "demo.lock"))
	acquired, err := contender.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		_ = contender.Unlock()
		t.Fatal("a second lifecycle acquired the app lock")
	}
}

func TestAcquireRejectsInvalidAppName(t *testing.T) {
	if _, err := Acquire(t.TempDir(), "../demo"); err == nil {
		t.Fatal("invalid app name was accepted")
	}
}
