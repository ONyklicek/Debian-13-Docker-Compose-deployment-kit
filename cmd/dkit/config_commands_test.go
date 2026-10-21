package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/presetcatalog"
)

func TestConfigCommandReadsAndAtomicallyUpdatesGlobalSettings(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "dkit.env")
	before := "# keep this comment\nACME_EMAIL=old@example.org\nCUSTOM_OPTION=preserved\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	input := strings.NewReader("ACME_EMAIL=ops@example.org\nSHARED_SERVICES=mariadb s3\n")
	if err := configCommand([]string{"set", "-"}, configDir, &output, input); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "# keep this comment") ||
		!strings.Contains(string(updated), "CUSTOM_OPTION=preserved") ||
		!strings.Contains(string(updated), "ACME_EMAIL=ops@example.org") {
		t.Fatalf("global config update did not preserve unrelated content: %s", updated)
	}

	output.Reset()
	if err := configCommand([]string{"get", "--json"}, configDir, &output, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ACME_EMAIL"] != "ops@example.org" || got["CUSTOM_OPTION"] != "preserved" {
		t.Fatalf("global settings JSON = %#v", got)
	}
}

func TestConfigCommandRejectsInvalidSettingsWithoutChangingFile(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "dkit.env")
	before := []byte("ACME_EMAIL=old@example.org\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	err := configCommand([]string{"set", "-"}, configDir, &bytes.Buffer{}, strings.NewReader("SHARED_SERVICES=redis\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported service") {
		t.Fatalf("config set error = %v, want unsupported shared service", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("invalid config changed dkit.env: %q -> %q", before, after)
	}
}

func TestEnvReplaceReplacesExistingEnvironment(t *testing.T) {
	configDir := t.TempDir()
	appPath := writeAppConfig(t, configDir, "shop", appConfigFixture)
	envPath := filepath.Join(filepath.Dir(appPath), "env")
	if err := os.WriteFile(envPath, []byte("KEEP=old\nREMOVE=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	input := strings.NewReader("KEEP='new value'\nTOKEN='a$secret'\n")
	if err := envCommand([]string{"replace", "shop"}, configDir, &output, input); err != nil {
		t.Fatal(err)
	}
	values, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appconfig.ParseEnv(values)
	if err != nil {
		t.Fatal(err)
	}
	if parsed["KEEP"] != "new value" || parsed["TOKEN"] != "a$secret" {
		t.Fatalf("replacement environment = %#v", parsed)
	}
	if _, exists := parsed["REMOVE"]; exists {
		t.Fatalf("removed key remains after replace: %#v", parsed)
	}
	if strings.Contains(output.String(), "a$secret") {
		t.Fatalf("replacement output leaked a secret: %q", output.String())
	}
}

func TestPresetAndTemplateCommandsSupportUIMutations(t *testing.T) {
	configDir := t.TempDir()
	var output bytes.Buffer
	if err := presetCommand([]string{"create", "custom-php", "--from", "php"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	custom, err := presetcatalog.Read(configDir, "custom-php")
	if err != nil {
		t.Fatal(err)
	}
	builtin, err := presetcatalog.Read(configDir, "php")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(custom.AppEnv, builtin.AppEnv) {
		t.Fatal("created preset did not copy built-in app.env")
	}
	output.Reset()
	if err := presetCommand([]string{"show", "custom-php", "--json"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	var shownPreset struct {
		Local  bool   `json:"local"`
		AppEnv string `json:"app_env"`
	}
	if err := json.Unmarshal(output.Bytes(), &shownPreset); err != nil {
		t.Fatal(err)
	}
	if !shownPreset.Local || shownPreset.AppEnv != string(custom.AppEnv) {
		t.Fatalf("preset show response = %#v", shownPreset)
	}
	if err := presetCommand([]string{"remove", "custom-php"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "presets", "custom-php")); !os.IsNotExist(err) {
		t.Fatalf("removed preset still exists: %v", err)
	}

	output.Reset()
	if err := templateCommand([]string{"create", "custom-build", "--from", "php"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	templates, err := listBuildTemplates(configDir)
	if err != nil {
		t.Fatal(err)
	}
	var localFound bool
	for _, template := range templates {
		if template.Name == "custom-build" && template.Local {
			localFound = true
		}
	}
	if !localFound {
		t.Fatalf("custom template missing from list: %#v", templates)
	}
	output.Reset()
	if err := templateCommand([]string{"show", "custom-build", "--json"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	var shownTemplate struct {
		Local   bool   `json:"local"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(output.Bytes(), &shownTemplate); err != nil {
		t.Fatal(err)
	}
	if !shownTemplate.Local || !strings.Contains(shownTemplate.Content, "FROM") {
		t.Fatalf("template show response = %#v", shownTemplate)
	}
	if err := templateCommand([]string{"reset", "custom-build"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
}
