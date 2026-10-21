package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/presetcatalog"
)

func TestPresetCommandTextAndJSON(t *testing.T) {
	configDir := t.TempDir()
	customFile := filepath.Join(configDir, "presets", "image", "app.env")
	if err := os.MkdirAll(filepath.Dir(customFile), 0o700); err != nil {
		t.Fatal(err)
	}
	before := []byte("# Custom image preset\nSOURCE=image\n")
	if err := os.WriteFile(customFile, before, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := presetCommand(nil, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "image [custom]") ||
		!strings.Contains(output.String(), "Custom image preset") ||
		!strings.Contains(output.String(), "dockerfile") {
		t.Fatalf("preset list output = %q", output.String())
	}

	output.Reset()
	if err := presetCommand([]string{"list", "--json"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	var presets []presetcatalog.Preset
	if err := json.Unmarshal(output.Bytes(), &presets); err != nil {
		t.Fatal(err)
	}
	if len(presets) != 6 {
		t.Fatalf("preset JSON count = %d, want 6: %#v", len(presets), presets)
	}
	var imageFound bool
	for _, preset := range presets {
		if preset.Name == "image" {
			imageFound = true
			if !preset.Local || preset.Description != "Custom image preset" {
				t.Errorf("image preset JSON = %#v", preset)
			}
		}
	}
	if !imageFound {
		t.Fatal("image preset not found")
	}
	after, err := os.ReadFile(customFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("preset list modified the user preset")
	}
}

func TestPresetCommandRejectsInvalidListArguments(t *testing.T) {
	for _, args := range [][]string{{"list", "--invalid"}, {"list", "--json", "extra"}} {
		var output bytes.Buffer
		if err := presetCommand(args, t.TempDir(), &output); err == nil {
			t.Errorf("presetCommand(%v) unexpectedly succeeded", args)
		}
	}
}

func TestTruncateDescriptionPreservesUTF8(t *testing.T) {
	got := truncateDescription("čřž", 2)
	if got != "čř" {
		t.Fatalf("truncateDescription = %q, want %q", got, "čř")
	}
}
