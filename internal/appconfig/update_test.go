package appconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdatePreservesCommentsAndValidatesBeforeReplace(t *testing.T) {
	configDir := t.TempDir()
	path := writeAppFile(t, configDir, "shop", "# keep this comment\nDOMAINS=shop.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/shop.git\nDOMAINS=old.example.com\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	app, err := Update(configDir, "shop", map[string]string{"WEB_PORT": "3000", "DOMAINS": "new.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !app.Valid || app.Values["WEB_PORT"] != "3000" || app.Values["DOMAINS"] != "new.example.com" {
		t.Fatalf("updated app = %#v", app)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, "DOMAINS=") != 1 || !strings.Contains(text, "# keep this comment") {
		t.Fatalf("updated config did not preserve comment/deduplicate key: %q", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("config permissions = %#o, want 0640", info.Mode().Perm())
	}

	before := append([]byte(nil), data...)
	if _, err := Update(configDir, "shop", map[string]string{"DOMAINS": "bad domain"}, nil); err == nil {
		t.Fatal("Update accepted invalid resulting app config")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid update changed config: before %q after %q", before, after)
	}
}

func TestUpdateAppliesUnsetAndRejectsUnsafeValues(t *testing.T) {
	configDir := t.TempDir()
	path := writeAppFile(t, configDir, "shop", "DOMAINS=shop.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/shop.git\nWEB_CMD=old\n")
	if _, err := Update(configDir, "shop", map[string]string{"WEB_CMD": "new value"}, []string{"GIT_BRANCH"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "WEB_CMD=old") || !strings.Contains(string(data), `WEB_CMD='new value'`) {
		t.Fatalf("updated config = %q", data)
	}
	if _, err := Update(configDir, "shop", map[string]string{"BAD-KEY": "x"}, nil); err == nil {
		t.Fatal("Update accepted invalid key")
	}
	if _, err := Update(configDir, "shop", map[string]string{"WEB_CMD": "first\nSOURCE=image"}, nil); err == nil {
		t.Fatal("Update accepted newline injection")
	}
}

func TestParseEnvComposeEscapingAndSecretNames(t *testing.T) {
	values, err := ParseEnv([]byte("PLAIN=value # note\nSINGLE='a $ value'\nDOUBLE=\"a $$b \\\\ c \\\"d\\\"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if values["PLAIN"] != "value" || values["SINGLE"] != "a $ value" || values["DOUBLE"] != `a $b \ c "d"` {
		t.Fatalf("parsed env = %#v", values)
	}
	for key, want := range map[string]bool{"APP_KEY": true, "DB_PASSWORD": true, "API_TOKEN": true, "MAIL_PASS": true, "APP_NAME": false} {
		if got := IsSecretEnvKey(key); got != want {
			t.Errorf("IsSecretEnvKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestUpdateEnvRoundTripsComposeValuesAndRequiresExistingValidApp(t *testing.T) {
	configDir := t.TempDir()
	writeAppFile(t, configDir, "shop", "DOMAINS=shop.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/shop.git\n")
	envPath := filepath.Join(configDir, "apps", "shop", "env")
	if err := os.WriteFile(envPath, []byte("# keep\nAPP_NAME=shop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := `two $ dollars, \slashes and "quotes" plus 'apostrophe'`
	values, err := UpdateEnv(configDir, "shop", map[string]string{"APP_KEY": want}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["APP_KEY"] != want {
		t.Fatalf("updated env value = %q, want %q", values["APP_KEY"], want)
	}
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "$$") || !strings.Contains(string(data), `\\slashes`) {
		t.Fatalf("Compose env escaping missing: %q", data)
	}
	if _, err := UpdateEnv(configDir, "missing", map[string]string{"A": "b"}, nil); err == nil {
		t.Fatal("UpdateEnv accepted missing app")
	}
}

func writeAppFile(t *testing.T, configDir, name, content string) string {
	t.Helper()
	dir := filepath.Join(configDir, "apps", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
