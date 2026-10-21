package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestValidGitRef(t *testing.T) {
	for _, ref := range []string{"main", "feature/release-1", "v2.4.0", "release_candidate"} {
		if !validGitRef(ref) {
			t.Errorf("validGitRef(%q) = false", ref)
		}
	}
	for _, ref := range []string{"", "-option", "main..evil", "main@{1}", "refs/heads/x;command"} {
		if validGitRef(ref) {
			t.Errorf("validGitRef(%q) = true", ref)
		}
	}
}

func TestResolveBuildDockerfileRejectsTraversalAndUsesTemplateOverrides(t *testing.T) {
	configDir, root, checkout, tempDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	app := appconfig.Validate("demo", map[string]string{"BUILD": "dockerfile", "DOCKERFILE": "../outside"})
	if _, _, err := resolveBuildDockerfile(app, configDir, root, checkout, tempDir); err == nil {
		t.Fatal("Dockerfile traversal was accepted")
	}

	templateDir := filepath.Join(configDir, "templates", "node")
	if err := os.MkdirAll(templateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	override := filepath.Join(templateDir, "Dockerfile")
	if err := os.WriteFile(override, []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app = appconfig.Validate("demo", map[string]string{"BUILD": "node"})
	path, cleanup, err := resolveBuildDockerfile(app, configDir, root, checkout, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil || path != override {
		t.Fatalf("override path = %s, cleanup=%v; want %s and no cleanup", path, cleanup != nil, override)
	}
}

func TestResolveBuildDockerfileUsesEmbeddedBuiltins(t *testing.T) {
	app := appconfig.Validate("demo", map[string]string{"BUILD": "php"})
	path, cleanup, err := resolveBuildDockerfile(app, t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if cleanup == nil {
		t.Fatal("embedded template did not return a cleanup function")
	}
	defer cleanup()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Generic PHP template") {
		t.Fatalf("unexpected embedded template contents: %q", data)
	}
}

func TestBuildRejectsSSHRepositoryWithoutRunningExternalTools(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=git\nGIT_REPOSITORY=git@example.com:team/demo.git\n")
	err := buildApp([]string{"demo"}, portable.Paths{Config: configDir, State: stateDir})
	if err == nil || !strings.Contains(err.Error(), "HTTPS repository URL") {
		t.Fatalf("buildApp error = %v, want unsupported SSH repository error", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps")); !os.IsNotExist(err) {
		t.Fatalf("rejected repository created state: stat error = %v", err)
	}
}

func TestFailedGitBuildKeepsCheckoutAndDoesNotReplaceBuildMetadata(t *testing.T) {
	installBuildHelpers(t)
	t.Setenv("DKIT_TEST_DOCKER_FAIL", "1")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/team/demo.git\n")
	if err := saveSettings(configDir, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONTEXT", "")
	paths := portable.Paths{Config: configDir, State: stateDir}
	err := buildApp([]string{"demo"}, paths)
	if err == nil || !strings.Contains(err.Error(), "checkout retained") {
		t.Fatalf("buildApp error = %v, want failed build checkout path", err)
	}
	buildsDir := filepath.Join(stateDir, "apps", "demo", "builds")
	entries, err := os.ReadDir(buildsDir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed build checkout not retained: entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps", "demo", "build.json")); !os.IsNotExist(err) {
		t.Fatalf("failed build wrote metadata: stat error = %v", err)
	}
}
