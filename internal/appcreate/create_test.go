package appcreate

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

func TestCreateBuiltInImagePreset(t *testing.T) {
	configDir := t.TempDir()
	appDir, err := Create(configDir, Options{
		Name: "status", Preset: "image", Domains: []string{"status.example.com", "www.example.com"},
		Image: "ghcr.io/example/status:1.2", Services: "redis", Port: "3001",
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := appconfig.Read(configDir, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !app.Valid {
		t.Fatalf("created app is invalid: %v", app.Errors)
	}
	if app.Values["DOMAINS"] != "status.example.com,www.example.com" {
		t.Errorf("DOMAINS = %q", app.Values["DOMAINS"])
	}
	if app.Values["IMAGE"] != "ghcr.io/example/status:1.2" ||
		app.Values["SERVICES"] != "redis" || app.Values["WEB_PORT"] != "3001" {
		t.Errorf("created values = %#v", app.Values)
	}
	for _, path := range []string{
		filepath.Join(appDir, "env"),
		filepath.Join(appDir, "ssh"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected generated path %s: %v", path, err)
		}
	}
}

func TestCreateLocalPresetExpandsAndProtectsSecrets(t *testing.T) {
	configDir := t.TempDir()
	presetDir := filepath.Join(configDir, "presets", "image")
	if err := os.MkdirAll(presetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	appTemplate := "# custom __APP__\nDOMAINS=__DOMAINS__\nSOURCE=git\nGIT_REPOSITORY=__REPO__\nGIT_BRANCH=__BRANCH__\n"
	if err := os.WriteFile(filepath.Join(presetDir, "app.env"), []byte(appTemplate), 0o600); err != nil {
		t.Fatal(err)
	}
	envTemplate := "APP=__APP__\nURL=https://__DOMAIN__\nHEX1=__HEX32__\nHEX2=__HEX32__\nLARAVEL=__LARAVEL_KEY__\n"
	if err := os.WriteFile(filepath.Join(presetDir, "env"), []byte(envTemplate), 0o600); err != nil {
		t.Fatal(err)
	}

	appDir, err := Create(configDir, Options{
		Name: "shop", Preset: "image", Domains: []string{"shop.example.com"},
		Repo: "https://example.com/shop.git", Branch: "release/1",
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := appconfig.Read(configDir, "shop")
	if err != nil || !app.Valid {
		t.Fatalf("created app check = valid %v, errors %v, read error %v", app.Valid, app.Errors, err)
	}
	env, err := os.ReadFile(filepath.Join(appDir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(env)
	for _, expected := range []string{"APP=shop", "URL=https://shop.example.com"} {
		if !strings.Contains(text, expected) {
			t.Errorf("expanded env %q does not contain %q", text, expected)
		}
	}
	hex1 := regexp.MustCompile(`HEX1=([0-9a-f]{64})`).FindStringSubmatch(text)
	hex2 := regexp.MustCompile(`HEX2=([0-9a-f]{64})`).FindStringSubmatch(text)
	if len(hex1) != 2 || len(hex2) != 2 || hex1[1] == hex2[1] {
		t.Fatalf("hex tokens were not uniquely generated: %q", text)
	}
	key := regexp.MustCompile(`LARAVEL=(base64:[A-Za-z0-9+/]{43}=)`).FindStringSubmatch(text)
	if len(key) != 2 {
		t.Fatalf("Laravel key was not generated: %q", text)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(key[1], "base64:"))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("generated Laravel key payload length = %d, decode error = %v", len(decoded), err)
	}
	if runtime.GOOS != "windows" {
		for name, want := range map[string]os.FileMode{"app.env": 0o640, "env": 0o600} {
			info, err := os.Stat(filepath.Join(appDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s permissions = %#o, want %#o", name, got, want)
			}
		}
	}
}

func TestCreateRejectsInvalidOrUnsafeInputsWithoutWriting(t *testing.T) {
	tests := []struct {
		name    string
		options Options
	}{
		{"missing preset", Options{Name: "shop", Domains: []string{"shop.example.com"}, Repo: "https://example.com/shop.git"}},
		{"missing domain", Options{Name: "shop", Preset: "dockerfile", Repo: "https://example.com/shop.git"}},
		{"missing repository", Options{Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"}}},
		{"missing image", Options{Name: "shop", Preset: "image", Domains: []string{"shop.example.com"}}},
		{"invalid domain", Options{Name: "shop", Preset: "dockerfile", Domains: []string{"bad domain"}, Repo: "https://example.com/shop.git"}},
		{"unsafe repository", Options{Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"}, Repo: "https://example.com/shop.git\nPUBLIC=0"}},
		{"invalid port", Options{Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"}, Repo: "https://example.com/shop.git", Port: "oops"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configDir := t.TempDir()
			if _, err := Create(configDir, test.options); err == nil {
				t.Fatal("Create unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(configDir, "apps")); !os.IsNotExist(err) {
				t.Fatalf("failed create wrote app files; stat error = %v", err)
			}
		})
	}
}

func TestCreateDoesNotOverwriteExistingApp(t *testing.T) {
	configDir := t.TempDir()
	appDir := filepath.Join(configDir, "apps", "shop")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(appDir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Create(configDir, Options{
		Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"},
		Repo: "https://example.com/shop.git",
	})
	if err == nil {
		t.Fatal("Create overwrote an existing app")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatalf("existing app marker changed: data %q, error %v", data, err)
	}
}

func TestCreatePublishesOnlyAfterAllFilesAreWritten(t *testing.T) {
	configDir := t.TempDir()
	var stagedPath string
	write := func(path string, data []byte, mode os.FileMode) error {
		stagedPath = filepath.Dir(path)
		if _, err := os.Stat(filepath.Join(configDir, "apps", "shop")); !os.IsNotExist(err) {
			t.Fatalf("app became visible before all staged files were written: %v", err)
		}
		return writeFile(path, data, mode)
	}
	appDir, err := create(configDir, Options{
		Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"},
		Repo: "https://example.com/shop.git",
	}, write)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(appDir) != filepath.Join(configDir, "apps") {
		t.Fatalf("created app path = %q", appDir)
	}
	if filepath.Dir(stagedPath) != filepath.Join(configDir, "apps") {
		t.Fatalf("staging directory was outside apps root: %q", stagedPath)
	}
}

func TestCreateCleansStagingDirectoryAfterWriteFailure(t *testing.T) {
	configDir := t.TempDir()
	write := func(_ string, _ []byte, _ os.FileMode) error {
		return errors.New("injected write failure")
	}
	_, err := create(configDir, Options{
		Name: "shop", Preset: "dockerfile", Domains: []string{"shop.example.com"},
		Repo: "https://example.com/shop.git",
	}, write)
	if err == nil || !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("create error = %v, want injected failure", err)
	}
	entries, err := os.ReadDir(filepath.Join(configDir, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed create left staging/app files: %#v", entries)
	}
}

func TestCreateRejectsSourceMismatchAndPathTraversal(t *testing.T) {
	for _, options := range []Options{
		{Name: "shop", Preset: "dockerfile", Source: "image", Domains: []string{"shop.example.com"}, Repo: "https://example.com/shop.git"},
		{Name: "../shop", Preset: "dockerfile", Domains: []string{"shop.example.com"}, Repo: "https://example.com/shop.git"},
	} {
		configDir := t.TempDir()
		if _, err := Create(configDir, options); err == nil {
			t.Errorf("Create(%#v) unexpectedly succeeded", options)
		}
		if _, err := os.Stat(filepath.Join(configDir, "apps")); !os.IsNotExist(err) {
			t.Errorf("invalid request created files: stat error = %v", err)
		}
	}
}
