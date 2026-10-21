package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

const appConfigFixture = `DOMAINS=shop.example.com
SOURCE=git
GIT_REPOSITORY=https://example.com/shop.git
BUILD=dockerfile
WEB_PORT=8080
HEALTH_PATH=/
SERVICES=mariadb,redis
`

func writeAppConfig(t *testing.T, configDir, name, config string) string {
	t.Helper()
	dir := filepath.Join(configDir, "apps", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListAppsTextAndJSON(t *testing.T) {
	configDir := t.TempDir()
	writeAppConfig(t, configDir, "shop", appConfigFixture)
	writeAppConfig(t, configDir, "broken", "DOMAINS=invalid\nSOURCE=git\n")

	var output bytes.Buffer
	if err := listApps(nil, configDir, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"shop.example.com", "shop", "valid", "broken", "invalid domain", appConfigValidationScope} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("app list output %q does not contain %q", output.String(), expected)
		}
	}

	output.Reset()
	if err := listApps([]string{"--json"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	var apps []map[string]any
	if err := json.Unmarshal(output.Bytes(), &apps); err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0]["name"] != "broken" || apps[0]["valid"] != false ||
		apps[1]["name"] != "shop" || apps[1]["valid"] != true {
		t.Fatalf("unexpected app list JSON: %#v", apps)
	}
}

func TestCheckAppValidInvalidAndMissing(t *testing.T) {
	configDir := t.TempDir()
	path := writeAppConfig(t, configDir, "shop", appConfigFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := checkApp([]string{"shop"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "is valid") || !strings.Contains(output.String(), appConfigValidationScope) {
		t.Fatalf("valid app check output = %q", output.String())
	}

	output.Reset()
	if err := checkApp([]string{"shop", "--json"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	var result appCheckResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Valid || result.Name != "shop" || len(result.Errors) != 0 {
		t.Fatalf("valid app check JSON = %#v", result)
	}

	writeAppConfig(t, configDir, "invalid", "DOMAINS=bad\nSOURCE=image\n")
	output.Reset()
	if err := checkApp([]string{"invalid"}, configDir, &output); err == nil {
		t.Fatal("checkApp accepted invalid config")
	}
	if !strings.Contains(output.String(), "is invalid") || !strings.Contains(output.String(), "IMAGE") {
		t.Fatalf("invalid app check output = %q", output.String())
	}

	output.Reset()
	if err := checkApp([]string{"missing", "--json"}, configDir, &output); err == nil {
		t.Fatal("checkApp accepted missing config")
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("missing app JSON output %q is invalid: %v", output.String(), err)
	}
	if result.Valid || len(result.Errors) == 0 {
		t.Fatalf("missing app check JSON = %#v", result)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("app check modified app.env")
	}
}

func TestAppCommandRejectsInvalidPortableArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"list", "--invalid"}, {"check"}, {"check", "shop", "--invalid"}} {
		var output bytes.Buffer
		if err := appCommand(args, portable.Paths{Config: t.TempDir()}, &output); err == nil {
			t.Errorf("appCommand(%v) unexpectedly succeeded", args)
		}
	}
}
