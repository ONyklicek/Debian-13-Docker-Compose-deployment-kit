package presetcatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListIncludesBuiltinsAndLocalPresets(t *testing.T) {
	configDir := t.TempDir()
	writePreset(t, configDir, "custom", "# Local template\nSOURCE=image\n")
	writePreset(t, configDir, "image", "# User image template\nSOURCE=image\n")
	if err := os.MkdirAll(filepath.Join(configDir, "presets", "without-config"), 0o700); err != nil {
		t.Fatal(err)
	}

	presets, err := List(configDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(presets))
	for i, preset := range presets {
		names[i] = preset.Name
		if i > 0 && presets[i-1].Name >= preset.Name {
			t.Fatalf("presets are not uniquely sorted: %#v", presets)
		}
	}
	wantNames := []string{"custom", "dockerfile", "image", "laravel", "node", "php", "static"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("preset names = %#v, want %#v", names, wantNames)
	}
	for _, preset := range presets {
		switch preset.Name {
		case "custom":
			if preset.Description != "Local template" || !preset.Local {
				t.Errorf("custom preset = %#v", preset)
			}
		case "image":
			if preset.Description != "User image template" || !preset.Local {
				t.Errorf("local image override = %#v", preset)
			}
		case "dockerfile":
			if preset.Description == "" || preset.Local {
				t.Errorf("built-in preset = %#v", preset)
			}
		case "without-config":
			t.Errorf("preset without app.env was listed: %#v", preset)
		}
	}
}

func TestListWithoutConfigDirectoryIsReadOnly(t *testing.T) {
	configDir := t.TempDir()
	presets, err := List(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 6 {
		t.Fatalf("built-in preset count = %d, want 6", len(presets))
	}
	if _, err := os.Stat(filepath.Join(configDir, "presets")); !os.IsNotExist(err) {
		t.Fatalf("List created local preset directory; stat error = %v", err)
	}
}

func writePreset(t *testing.T, configDir, name, content string) {
	t.Helper()
	dir := filepath.Join(configDir, "presets", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
