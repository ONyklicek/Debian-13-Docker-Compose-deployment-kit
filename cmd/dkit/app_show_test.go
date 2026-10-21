package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppShowReportsConfigurationAndExplicitRuntimeBoundary(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com,www.demo.example.com\nSOURCE=image\nIMAGE=ghcr.io/example/demo:1\nSERVICES=postgres,redis\nWORKERS=queue\nWORKER_queue_CMD=php artisan queue:work\n")
	buildDir := filepath.Join(stateDir, "apps", "demo")
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "build.json"), []byte(`{"image":"ghcr.io/example/demo:1","revision":"abc123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := showApp([]string{"demo"}, configDir, stateDir, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo.example.com", "ghcr.io/example/demo:1", "Revision:  abc123", "not queried", "no Docker operations"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("text output %q missing %q", output.String(), want)
		}
	}

	output.Reset()
	if err := showApp([]string{"demo", "--json"}, configDir, stateDir, &output); err != nil {
		t.Fatal(err)
	}
	var details appDetails
	if err := json.Unmarshal(output.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	if details.Name != "demo" || !details.ConfigValid || details.RuntimeChecked ||
		details.Revision != "abc123" || len(details.Services) != 2 || len(details.Workers) != 1 {
		t.Fatalf("details = %#v", details)
	}
	if !strings.Contains(details.RuntimeScope, "were not queried") {
		t.Fatalf("runtime boundary = %q", details.RuntimeScope)
	}
	if !strings.Contains(details.Validation, "Static app.env checks only") {
		t.Fatalf("validation scope = %q", details.Validation)
	}
}

func TestAppShowRejectsInvalidArgumentsAndSurfacesCorruptBuildMetadata(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	if err := showApp(nil, configDir, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("missing app name was accepted")
	}
	if err := showApp([]string{"demo", "--unknown"}, configDir, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown option was accepted")
	}
	buildDir := filepath.Join(stateDir, "apps", "demo")
	if err := os.MkdirAll(buildDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildDir, "build.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := showApp([]string{"demo", "--json"}, configDir, stateDir, &bytes.Buffer{}); err == nil {
		t.Fatal("corrupt build metadata was silently ignored")
	}
}
