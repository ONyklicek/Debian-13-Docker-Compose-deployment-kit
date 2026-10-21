package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestDBCommandUsesDedicatedComposeDatabase(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "desktop-linux")

	for _, tt := range []struct {
		service, action, shellCmd string
	}{
		{"mariadb", "shell", `exec mariadb -u "$MARIADB_USER" -p"$MARIADB_PASSWORD" "$MARIADB_DATABASE"`},
		{"postgres", "dump", `export PGPASSWORD="$POSTGRES_PASSWORD"; exec pg_dump --no-owner -U "$POSTGRES_USER" "$POSTGRES_DB"`},
		{"postgres", "import", `exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" "$POSTGRES_DB"`},
	} {
		t.Run(tt.service+"-"+tt.action, func(t *testing.T) {
			configDir, stateDir := t.TempDir(), t.TempDir()
			writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES="+tt.service+"\n")
			appDir := filepath.Join(configDir, "apps", "demo")
			if err := os.WriteFile(filepath.Join(appDir, "services.env"), []byte("POSTGRES_PASSWORD=secret\nMARIADB_PASSWORD=secret\nMARIADB_ROOT_PASSWORD=root-secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			paths := portable.Paths{Config: configDir, State: stateDir}
			if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
				t.Fatal(err)
			}

			commandArgs := []string{tt.action, "demo"}
			if tt.action == "import" {
				commandArgs = append(commandArgs, "--yes")
			}
			if err := dbCommand(commandArgs, paths); err != nil {
				t.Fatal(err)
			}
			gotBytes, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if err := json.Unmarshal(gotBytes, &got); err != nil {
				t.Fatal(err)
			}
			want := []string{
				"--context", "desktop-linux", "compose",
				"--env-file", filepath.Join(appDir, "services.env"),
				"--project-name", "dkit-demo", "--file", filepath.Join(stateDir, "apps", "demo", "compose.yaml"),
				"exec", "-T",
			}
			if tt.action == "shell" {
				want = append(want, "-i", tt.service, "sh", "-c", dbShellCommand(tt.service))
			} else {
				want = append(want, "-i", tt.service, "sh", "-c", tt.shellCmd)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Docker args = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDBMigrateRequiresConfirmationAndUsesSelectedComposeApp(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "desktop-linux")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\nMIGRATE_CMD=\"php artisan migrate --force\"\nMIGRATE_USER=1000:1000\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := dbCommand([]string{"migrate", "demo"}, paths); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("unconfirmed migration error = %v, want explicit confirmation", err)
	}
	if err := dbCommand([]string{"migrate", "demo", "--yes"}, paths); err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--context", "desktop-linux", "compose",
		"--env-file", filepath.Join(configDir, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", filepath.Join(stateDir, "apps", "demo", "compose.yaml"),
		"run", "--rm", "-T", "--user", "1000:1000", "app", "sh", "-c", "php artisan migrate --force",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
}

func TestDBCommandValidatesArgumentsAndRequirements(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	paths := portable.Paths{Config: configDir, State: stateDir}
	for _, args := range [][]string{{}, {"shell"}, {"shell", "missing"}} {
		if err := dbCommand(args, paths); err == nil {
			t.Errorf("dbCommand(%v) unexpectedly succeeded", args)
		}
	}
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\n")
	if err := dbCommand([]string{"shell", "demo"}, paths); err == nil {
		t.Fatal("dbCommand accepted app without database service")
	}
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\n")
	if err := os.MkdirAll(filepath.Join(stateDir, "apps", "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "apps", "demo", "compose.yaml"), []byte("name: dkit-demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "apps", "demo", "services.env"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := dbCommand([]string{"dump", "demo"}, paths); err == nil {
		t.Fatal("dbCommand accepted missing database credentials")
	}
	if err := dbCommand([]string{"migrate", "demo", "--yes"}, paths); err == nil {
		t.Fatal("dbCommand accepted migration without MIGRATE_CMD")
	}
}
