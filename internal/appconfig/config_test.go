package appconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validGitConfig = `# Docker kit app config
DOMAINS="shop.example.com, www.example.com"
SOURCE=git
GIT_REPOSITORY=git@github.com:org/shop.git
GIT_BRANCH=main
BUILD=php
WEB_PORT=8080
HEALTH_PATH=/up
SERVICES="mariadb redis"
WORKERS="queue scheduler"
WORKER_queue_CMD="php artisan queue:work --tries=3"
WORKER_scheduler_CMD="php artisan schedule:work"
PERSIST="storage:/var/www/html/storage"
PERSIST_OWNER=33:33
MIGRATE_USER=www-data
SHARED_SERVICES="mariadb postgres s3"
MEMORY_LIMIT=512m
`

func TestParseConfigConventions(t *testing.T) {
	values, err := Parse([]byte("  # comment\r\nexport DOMAIN=first.example\r\n" +
		"DOMAIN=second.example # trailing note\r\nQUOTED='a # literal'\r\n" +
		"DOUBLE=\"a \\\"quoted\\\" value\"\r\nINLINE=hash#part\r\nEMPTY=\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DOMAIN": "second.example", "QUOTED": "a # literal",
		"DOUBLE": `a "quoted" value`, "INLINE": "hash#part", "EMPTY": "",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("Parse() = %#v, want %#v", values, want)
	}
}

func TestParseRejectsMalformedLines(t *testing.T) {
	for _, input := range []string{"not a setting\n", "BAD-KEY=value\n", "export \n"} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", input)
		}
	}
}

func TestValidateValidGitAndImageConfigurations(t *testing.T) {
	gitValues, err := Parse([]byte(validGitConfig))
	if err != nil {
		t.Fatal(err)
	}
	gitApp := Validate("shop", gitValues)
	if !gitApp.Valid {
		t.Fatalf("valid Git app rejected: %v", gitApp.Errors)
	}
	if !reflect.DeepEqual(gitApp.Domains, []string{"shop.example.com", "www.example.com"}) {
		t.Fatalf("domains = %#v", gitApp.Domains)
	}
	if !reflect.DeepEqual(gitApp.Services, []string{"mariadb", "redis"}) {
		t.Fatalf("services = %#v", gitApp.Services)
	}

	imageValues, err := Parse([]byte("DOMAINS=status.example.com\nSOURCE=image\nIMAGE=ghcr.io/org/status:1\nPUBLIC=1\n"))
	if err != nil {
		t.Fatal(err)
	}
	imageApp := Validate("status", imageValues)
	if !imageApp.Valid {
		t.Fatalf("valid image app rejected: %v", imageApp.Errors)
	}

	internalValues, err := Parse([]byte("PUBLIC=0\nSOURCE=git\nGIT_REPOSITORY=./src\n"))
	if err != nil {
		t.Fatal(err)
	}
	if app := Validate("worker", internalValues); !app.Valid {
		t.Fatalf("valid internal app rejected: %v", app.Errors)
	}
}

func TestValidateAllowsDuplicateWorkerNames(t *testing.T) {
	values, err := Parse([]byte(strings.Replace(validGitConfig, `WORKERS="queue scheduler"`, `WORKERS="queue queue"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if app := Validate("shop", values); !app.Valid {
		t.Fatalf("duplicate worker names should follow legacy validation behavior: %v", app.Errors)
	}
}

func TestValidateReportsInvalidFields(t *testing.T) {
	values, err := Parse([]byte(`DOMAINS="Bad Domain,valid.example.com"
SOURCE=git
BUILD=PHP!
WEB_PORT=8
PUBLISH_PORT=65536
HEALTH_PATH=not-absolute
SERVICES=mariadb,unknown
WORKERS="queue queue web"
WORKER_queue_PATH=/socket
WORKER_web_CMD=run
PERSIST="bad path data:/data copy:/data data:/cache"
PERSIST_OWNER=x:y
SHARED_SERVICES=redis
DB_NAME=Bad-DB
S3_BUCKET=x
S3_DOMAIN=assets.example.com
MEMORY_LIMIT=512mb
`))
	if err != nil {
		t.Fatal(err)
	}

	app := Validate("Bad_Name", values)
	if app.Valid || len(app.Errors) < 12 {
		t.Fatalf("invalid app should return multiple validation errors, got valid=%v errors=%v", app.Valid, app.Errors)
	}
	for _, expected := range []string{
		"invalid app name", "invalid domain", "GIT_REPOSITORY",
		"invalid BUILD", "WEB_PORT", "PUBLISH_PORT", "HEALTH_PATH", "unknown service",
		"reserved", "PATH requires", "invalid PERSIST",
		"duplicate PERSIST volume name", "duplicate PERSIST container path",
		"PERSIST_OWNER", "cannot be shared", "DB_NAME", "invalid S3_BUCKET",
		"requires service s3", "MEMORY_LIMIT",
	} {
		found := false
		for _, message := range app.Errors {
			if strings.Contains(message, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected validation error containing %q; got %v", expected, app.Errors)
		}
	}
}

func TestValidateAcceptsOptionalPublishPort(t *testing.T) {
	values, err := Parse([]byte("DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nPUBLISH_PORT=3001\n"))
	if err != nil {
		t.Fatal(err)
	}
	if app := Validate("demo", values); !app.Valid {
		t.Fatalf("valid PUBLISH_PORT rejected: %v", app.Errors)
	}
}

func TestListAppsUsesPortableConfigDirectoryAndReportsInvalidConfig(t *testing.T) {
	configDir := t.TempDir()
	writeApp(t, configDir, "valid", validGitConfig)
	writeApp(t, configDir, "broken", "DOMAINS=broken\nSOURCE=git\n")
	writeApp(t, configDir, "bad.name", "PUBLIC=0\n")
	if err := os.MkdirAll(filepath.Join(configDir, "apps", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apps", "not-a-directory"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	apps, err := List(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 {
		t.Fatalf("List returned %d apps, want 3 (directories without app.env are ignored): %#v", len(apps), apps)
	}
	if apps[0].Name != "bad.name" || apps[0].Valid {
		t.Errorf("invalid directory name summary = %#v", apps[0])
	}
	if apps[1].Name != "broken" || apps[1].Valid || len(apps[1].Errors) == 0 {
		t.Errorf("invalid app config summary = %#v", apps[1])
	}
	if apps[2].Name != "valid" || !apps[2].Valid {
		t.Errorf("valid app summary = %#v", apps[2])
	}
	if _, err := Read(configDir, ".."); err == nil {
		t.Fatal("Read accepted a path-traversal app name")
	}
}

func TestListAppsMissingDirectoryIsEmptyAndReadIsReadOnly(t *testing.T) {
	configDir := t.TempDir()
	apps, err := List(configDir)
	if err != nil || len(apps) != 0 {
		t.Fatalf("List on missing apps directory = %#v, %v", apps, err)
	}
	writeApp(t, configDir, "shop", validGitConfig)
	path, err := AppFilePath(configDir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read(configDir, "shop"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("Read changed the app.env file")
	}
}

func writeApp(t *testing.T, configDir, name, content string) {
	t.Helper()
	dir := filepath.Join(configDir, "apps", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
