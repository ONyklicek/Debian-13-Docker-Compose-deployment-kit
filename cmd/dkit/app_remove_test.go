package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestRemoveAppArchivesConfigAndPreservesState(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "postgres")
	if err := saveSettings(paths.Config, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := removeAppCommand([]string{"demo", "--yes"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "local state and Compose volumes were preserved") {
		t.Fatalf("remove output = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(paths.Config, "apps", "demo")); !os.IsNotExist(err) {
		t.Fatalf("app remains registered: %v", err)
	}
	archives, err := filepath.Glob(filepath.Join(paths.Config, "apps-removed", "demo.*", "app.env"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archived config paths = %v, err=%v", archives, err)
	}
	if _, err := os.Stat(filepath.Join(paths.State, "apps", "demo", "compose.yaml")); err != nil {
		t.Fatalf("app state was not preserved: %v", err)
	}
	assertCapturedDockerArgs(t, capture, []string{
		"--context", "desktop-linux", "compose", "--env-file", filepath.Join(paths.Config, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", filepath.Join(paths.State, "apps", "demo", "compose.yaml"),
		"down", "--remove-orphans",
	})
}

func TestRemoveAppPurgeRequiresConfirmationAndRemovesLocalState(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "postgres")
	if err := saveSettings(paths.Config, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if err := removeAppCommand([]string{"demo", "--purge"}, paths, &bytes.Buffer{}); err == nil {
		t.Fatal("purge succeeded without explicit confirmation")
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("Docker ran without explicit confirmation: %v", err)
	}
	var output bytes.Buffer
	if err := removeAppCommand([]string{"demo", "--yes", "--purge"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Compose-managed volumes") {
		t.Fatalf("purge output = %q", output.String())
	}
	for _, path := range []string{
		filepath.Join(paths.Config, "apps", "demo"),
		filepath.Join(paths.State, "apps", "demo"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("purge left %s in place: %v", path, err)
		}
	}
	assertCapturedDockerArgs(t, capture, []string{
		"--context", "desktop-linux", "compose", "--env-file", filepath.Join(paths.Config, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", filepath.Join(paths.State, "apps", "demo", "compose.yaml"),
		"down", "--remove-orphans", "--volumes",
	})
}

func TestRemoveAppDockerFailureLeavesConfigurationAndState(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DKIT_TEST_DOCKER_FAIL_MATCH", "down")
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "postgres")

	err := removeAppCommand([]string{"demo", "--yes"}, paths, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "stop app") {
		t.Fatalf("remove error = %v, want Compose failure", err)
	}
	for _, path := range []string{
		filepath.Join(paths.Config, "apps", "demo", "app.env"),
		filepath.Join(paths.State, "apps", "demo", "compose.yaml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed removal changed %s: %v", path, err)
		}
	}
}

func TestRemoveAppRequiresPortableComposeModel(t *testing.T) {
	paths := portable.Paths{Config: t.TempDir(), State: t.TempDir()}
	writeAppConfig(t, paths.Config, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	err := removeAppCommand([]string{"demo", "--yes"}, paths, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "Compose model") {
		t.Fatalf("remove error = %v, want missing Compose model", err)
	}
}

func TestRemoveAppRejectsUnconfirmedAndInvalidInput(t *testing.T) {
	paths := portable.Paths{Config: t.TempDir(), State: t.TempDir()}
	for _, args := range [][]string{
		{"demo"}, {"../outside", "--yes"}, {"demo", "--yes", "--unknown"},
	} {
		if err := removeAppCommand(args, paths, &bytes.Buffer{}); err == nil {
			t.Errorf("removeAppCommand(%v) unexpectedly succeeded", args)
		}
	}
}

func assertCapturedDockerArgs(t *testing.T, capture string, suffix []string) {
	t.Helper()
	argsData, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(argsData, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, suffix) {
		t.Fatalf("Docker args = %#v, want %#v", got, suffix)
	}
}
