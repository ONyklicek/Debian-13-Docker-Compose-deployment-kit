package deploymentstate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	want := State{
		ActiveSlot:           "green",
		PreviousSlot:         "blue",
		ActiveImage:          "registry.example/app:2",
		PreviousImage:        "registry.example/app:1",
		ActiveRevision:       "revision-2",
		PrevRevision:         "revision-1",
		DockerContext:        "desktop-linux",
		DeployedAt:           "2026-06-02T12:00:00Z",
		ComposeSHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ActiveInputsSHA256:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PreviousInputsSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if err := Save(dir, "sample", want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(dir, "sample")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, "apps", "sample", "deployment.json"))
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestLoadMissingState(t *testing.T) {
	got, err := Load(t.TempDir(), "sample")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != (State{}) {
		t.Fatalf("Load() = %#v, want empty state", got)
	}
}

func TestLoadRejectsInvalidStateAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path, err := statePath(dir, "sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"active_slot":"red","active_image":"app:1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, "sample"); err == nil {
		t.Fatal("Load() accepted invalid active slot")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "outside.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load(dir, "sample"); err == nil {
		t.Fatal("Load() accepted symlink")
	}
}

func TestSaveRejectsInconsistentState(t *testing.T) {
	err := Save(t.TempDir(), "sample", State{ActiveSlot: "blue"})
	if err == nil {
		t.Fatal("Save() accepted an active slot without an image")
	}
}

func TestSaveRejectsMissingOrMalformedComposeChecksum(t *testing.T) {
	for _, checksum := range []string{
		"",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg",
	} {
		state := State{
			ActiveSlot: "blue", ActiveImage: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ComposeSHA256:      checksum,
			ActiveInputsSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}
		if err := Save(t.TempDir(), "sample", state); err == nil {
			t.Errorf("Save() accepted ComposeSHA256 %q", checksum)
		}
	}
}

func TestSaveRejectsMissingOrMalformedInputChecksums(t *testing.T) {
	valid := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tt := range []struct {
		name         string
		active       string
		previous     string
		previousSlot string
	}{
		{name: "missing active"},
		{name: "malformed active", active: "not-a-checksum"},
		{name: "missing previous", active: valid, previousSlot: "green"},
		{name: "malformed previous", active: valid, previous: "not-a-checksum", previousSlot: "green"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			previousImage := ""
			if tt.previousSlot != "" {
				previousImage = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
			}
			state := State{
				ActiveSlot: "blue", ActiveImage: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PreviousSlot: tt.previousSlot, PreviousImage: previousImage,
				ComposeSHA256: strings.Repeat("a", 64), ActiveInputsSHA256: tt.active,
				PreviousInputsSHA256: tt.previous,
			}
			if err := Save(t.TempDir(), "sample", state); err == nil {
				t.Fatal("Save() accepted invalid input checksums")
			}
		})
	}
}
