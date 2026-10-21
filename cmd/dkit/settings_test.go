package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveAndLoadSettings(t *testing.T) {
	configDir := t.TempDir()
	want := settings{DockerContext: "desktop-linux"}
	if err := saveSettings(configDir, want); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(configDir, settings{DockerContext: "remote-prod"}); err != nil {
		t.Fatalf("replace saved settings: %v", err)
	}
	got, err := loadSettings(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.DockerContext != "remote-prod" {
		t.Fatalf("DockerContext = %q, want %q", got.DockerContext, "remote-prod")
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.json")); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
}

func TestLoadSettingsRejectsMalformedJSON(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(configDir); err == nil {
		t.Fatal("loadSettings unexpectedly accepted malformed JSON")
	}
}

func TestContextArgs(t *testing.T) {
	if got, want := contextArgs("desktop-linux"), []string{"--context", "desktop-linux"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("contextArgs() = %#v, want %#v", got, want)
	}
	if got := contextArgs(""); len(got) != 0 {
		t.Fatalf("contextArgs with no selection = %#v, want no arguments", got)
	}
}

func TestResolveContext(t *testing.T) {
	t.Run("environment wins", func(t *testing.T) {
		got, err := resolveContext("environment", "saved", func() (string, error) {
			t.Fatal("active Docker context should not be queried")
			return "", nil
		})
		if err != nil || got != "environment" {
			t.Fatalf("resolveContext() = %q, %v; want environment, nil", got, err)
		}
	})
	t.Run("saved value wins over Docker current", func(t *testing.T) {
		got, err := resolveContext("", "saved", func() (string, error) {
			t.Fatal("active Docker context should not be queried")
			return "", nil
		})
		if err != nil || got != "saved" {
			t.Fatalf("resolveContext() = %q, %v; want saved, nil", got, err)
		}
	})
	t.Run("Docker current is used when no override exists", func(t *testing.T) {
		got, err := resolveContext("", "", func() (string, error) { return "desktop-linux\n", nil })
		if err != nil || got != "desktop-linux" {
			t.Fatalf("resolveContext() = %q, %v; want desktop-linux, nil", got, err)
		}
	})
	t.Run("Docker current lookup error is returned", func(t *testing.T) {
		wantErr := errors.New("docker CLI unavailable")
		_, err := resolveContext("", "", func() (string, error) { return "", wantErr })
		if !errors.Is(err, wantErr) {
			t.Fatalf("resolveContext error = %v, want %v", err, wantErr)
		}
	})
	t.Run("empty Docker current is rejected", func(t *testing.T) {
		if _, err := resolveContext("", "", func() (string, error) { return " \n", nil }); err == nil {
			t.Fatal("resolveContext accepted an empty active context")
		}
	})
}
