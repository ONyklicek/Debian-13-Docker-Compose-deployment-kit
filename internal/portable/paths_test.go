package portable

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathsForLinuxAndMac(t *testing.T) {
	empty := func(string) string { return "" }
	tests := []struct {
		goos, stateSuffix string
	}{
		{"linux", filepath.Join(".local", "share", "dkit")},
		{"darwin", filepath.Join("Library", "Application Support", "dkit")},
	}
	for _, tt := range tests {
		got := pathsFor(tt.goos, "/home/user", "/home/user/.config", "/home/user/.cache", empty)
		wantConfig := filepath.Join("/home/user", ".config", "dkit")
		wantState := filepath.Join("/home/user", tt.stateSuffix)
		if got.Config != wantConfig || got.State != wantState {
			t.Errorf("%s paths = %#v, want config %q and state %q", tt.goos, got, wantConfig, wantState)
		}
	}
}

func TestPathsForXDGDataHome(t *testing.T) {
	dataHome := "/mnt/data"
	if runtime.GOOS == "windows" {
		dataHome = filepath.Join("C:\\", "mnt", "data")
	}
	getenv := func(key string) string {
		if key == "XDG_DATA_HOME" {
			return dataHome
		}
		return ""
	}
	got := pathsFor("linux", "/home/user", "/home/user/.config", "/home/user/.cache", getenv)
	if got.State != filepath.Join(dataHome, "dkit") {
		t.Fatalf("XDG data path = %q", got.State)
	}
}

func TestPathsForWindowsAndOverrides(t *testing.T) {
	localAppData := filepath.Join("C:\\", "Users", "user", "AppData", "Local")
	getenv := func(key string) string {
		if key == "LOCALAPPDATA" {
			return localAppData
		}
		return ""
	}
	got := pathsFor("windows", filepath.Join("C:\\", "Users", "user"),
		filepath.Join("C:\\", "Users", "user", "AppData", "Roaming"), localAppData, getenv)
	if got.Config != filepath.Join("C:\\", "Users", "user", "AppData", "Roaming", "dkit") ||
		got.State != filepath.Join(localAppData, "dkit") {
		t.Fatalf("Windows paths = %#v", got)
	}

	getenv = func(key string) string {
		switch key {
		case "DKIT_CONFIG_DIR":
			return "/custom/config"
		case "DKIT_ETC":
			return "/legacy/config"
		case "DKIT_STATE_DIR":
			return "/custom/state"
		case "DKIT_STATE":
			return "/legacy/state"
		}
		return ""
	}
	got = pathsFor("linux", "/home/user", "/home/user/.config", "/home/user/.cache", getenv)
	if got.Config != filepath.Clean("/custom/config") || got.State != filepath.Clean("/custom/state") {
		t.Fatalf("override paths = %#v", got)
	}
}
