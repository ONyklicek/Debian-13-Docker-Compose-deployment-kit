package appclone

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

const cloneFixture = `# source app
DOMAINS=old.example.com
SOURCE=image
IMAGE=example/app:latest
SERVICES="postgres redis s3"
DB_NAME=source_db
DB_USER=source_user
S3_BUCKET=source-bucket
S3_DOMAIN=assets.old.example.com
PUBLISH_PORT=13000
PERSIST=uploads:/srv/uploads
`

func makeSource(t *testing.T, configDir string) string {
	t.Helper()
	dir := filepath.Join(configDir, "apps", "source")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(cloneFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("# app secrets\nAPP_KEY=source-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services.env"), []byte("POSTGRES_PASSWORD=source-db-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCloneCopiesApplicationEnvButNotServiceCredentials(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := makeSource(t, configDir)
	path, err := Clone(configDir, Options{Source: "source", Name: "copy", Domains: []string{"new.example.com", "www.new.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(configDir, "apps", "copy") {
		t.Fatalf("clone path = %q", path)
	}
	data, err := os.ReadFile(filepath.Join(path, "app.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"DOMAINS=new.example.com,www.new.example.com",
		"SOURCE=image",
		"IMAGE=example/app:latest",
		"PERSIST=uploads:/srv/uploads",
	} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("cloned app.env missing %q:\n%s", expected, data)
		}
	}
	for _, removed := range []string{"source_db", "source_user", "source-bucket", "assets.old.example.com", "PUBLISH_PORT"} {
		if strings.Contains(string(data), removed) {
			t.Errorf("cloned app.env retained source-specific value %q:\n%s", removed, data)
		}
	}
	if app, err := appconfig.Read(configDir, "copy"); err != nil || !app.Valid {
		t.Fatalf("cloned app validation = %#v, %v", app, err)
	}
	env, err := os.ReadFile(filepath.Join(path, "env"))
	if err != nil || string(env) != "# app secrets\nAPP_KEY=source-secret\n" {
		t.Fatalf("cloned env = %q, error %v", env, err)
	}
	if _, err := os.Stat(filepath.Join(path, "services.env")); !os.IsNotExist(err) {
		t.Fatalf("service credentials were copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "services.env")); err != nil {
		t.Fatalf("source credentials were unexpectedly changed: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(path, "env"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("cloned env permissions = %v, %v", info, err)
		}
	}
}

func TestCloneCanOmitEnvironmentAndRejectUnsafeOrUnsupportedSources(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := makeSource(t, configDir)
	if _, err := Clone(configDir, Options{Source: "source", Name: "noenv", Domains: []string{"new.example.com"}, NoEnv: true}); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(filepath.Join(configDir, "apps", "noenv", "env"))
	if err != nil || len(env) != 0 {
		t.Fatalf("--no-env result = %q, error %v", env, err)
	}
	if _, err := Clone(configDir, Options{Source: "source", Name: "no-domain"}); err == nil {
		t.Fatal("Clone accepted a missing domain")
	}
	if _, err := Clone(configDir, Options{Source: "../source", Name: "traversal", Domains: []string{"new.example.com"}}); err == nil {
		t.Fatal("Clone accepted source path traversal")
	}
	if _, err := Clone(configDir, Options{Source: "source", Name: "noenv", Domains: []string{"other.example.com"}}); err == nil {
		t.Fatal("Clone replaced an existing destination")
	}

	override := filepath.Join(sourceDir, "compose.override.yaml")
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(configDir, Options{Source: "source", Name: "override-copy", Domains: []string{"new.example.com"}}); err == nil || !strings.Contains(err.Error(), "does not support overrides") {
		t.Fatalf("override clone error = %v", err)
	}
}

func TestCloneRejectsGitSourceWithSSHCredentials(t *testing.T) {
	configDir := t.TempDir()
	sourceDir := filepath.Join(configDir, "apps", "source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "app.env"), []byte("DOMAINS=old.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/repo.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "ssh", "deploy_key"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Clone(configDir, Options{Source: "source", Name: "copy", Domains: []string{"new.example.com"}}); err == nil || !strings.Contains(err.Error(), "SSH deployment credentials") {
		t.Fatalf("SSH clone error = %v", err)
	}
}
