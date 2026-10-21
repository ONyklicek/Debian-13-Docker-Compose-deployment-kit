package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func testRenderedApp(t *testing.T, services string) (portable.Paths, string) {
	t.Helper()
	configDir, stateDir := t.TempDir(), t.TempDir()
	appEnv := "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\n"
	if services != "" {
		appEnv += "SERVICES=" + services + "\n"
	}
	writeAppConfig(t, configDir, "demo", appEnv)
	servicesEnv := filepath.Join(configDir, "apps", "demo", "services.env")
	if services != "" {
		if err := os.WriteFile(servicesEnv, []byte("POSTGRES_PASSWORD=secret\nREDIS_PASSWORD=secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	return paths, filepath.Join(stateDir, "apps", "demo", "compose.yaml")
}

func TestLogsCommandUsesComposeContextAndOptions(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "remote-test")
	paths, composePath := testRenderedApp(t, "postgres redis")
	if err := logsCommand([]string{"demo", "-f", "--tail", "50", "postgres"}, paths); err != nil {
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
	want := []string{"--context", "remote-test", "compose",
		"--env-file", filepath.Join(paths.Config, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", composePath,
		"logs", "--timestamps", "--tail=50", "--follow", "postgres"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
}

func TestExecCommandUsesSelectedServiceAndArgv(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, composePath := testRenderedApp(t, "postgres")
	if err := execAppCommand([]string{"demo", "--", "php", "-v"}, paths); err != nil {
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
	want := []string{"--context", "default", "compose",
		"--env-file", filepath.Join(paths.Config, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", composePath,
		"exec", "-T", "-i", "app", "php", "-v"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
}

func TestLogsAndExecRejectUnknownService(t *testing.T) {
	paths, _ := testRenderedApp(t, "")
	if err := logsCommand([]string{"demo", "missing"}, paths); err == nil {
		t.Fatal("logsCommand accepted an unknown service")
	}
	if err := execAppCommand([]string{"demo", "missing", "--", "sh"}, paths); err == nil {
		t.Fatal("execAppCommand accepted an unknown service")
	}
}
