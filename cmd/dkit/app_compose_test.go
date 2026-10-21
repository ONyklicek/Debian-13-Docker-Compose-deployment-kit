package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestAppServiceExistsIncludesDeclaredWorkers(t *testing.T) {
	app := appconfig.App{Values: map[string]string{"WORKERS": "queue, scheduler"}}
	for _, service := range []string{"queue", "scheduler"} {
		if !appServiceExists(app, service) {
			t.Errorf("appServiceExists(%q) = false, want true", service)
		}
	}
	if appServiceExists(app, "unknown") {
		t.Fatal("appServiceExists accepted an undeclared worker")
	}
}

func TestAppComposeRenderAndWrite(t *testing.T) {
	configDir := t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	stateDir := t.TempDir()
	paths := portable.Paths{Config: configDir, State: stateDir}

	var output bytes.Buffer
	if err := appCommand([]string{"compose", "render", "demo"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `image: "nginx:1.27"`) {
		t.Fatalf("render output = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "apps", "demo", "compose.yaml")); !os.IsNotExist(err) {
		t.Fatalf("render without --write created a file: %v", err)
	}

	output.Reset()
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `image: "nginx:1.27"`) ||
		!strings.Contains(output.String(), path) ||
		!strings.Contains(output.String(), "No Docker operations") {
		t.Fatalf("write result %q; file %q", output.String(), data)
	}
}

func TestAppComposeWritePreparesDedicatedServiceFiles(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nSERVICES=postgres\n")
	var output bytes.Buffer
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, portable.Paths{Config: configDir, State: stateDir}, &output); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(configDir, "apps", "demo", "services.env"),
		filepath.Join(stateDir, "apps", "demo", "runtime.env"),
		filepath.Join(stateDir, "apps", "demo", "compose.yaml"),
	} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("expected regular generated file %s; info=%v err=%v", path, info, err)
		}
	}
	if !strings.Contains(output.String(), "No Docker operations") {
		t.Fatalf("write output = %q", output.String())
	}
}

func TestAppComposeRejectsInvalidArguments(t *testing.T) {
	paths := portable.Paths{Config: t.TempDir(), State: t.TempDir()}
	for _, args := range [][]string{
		{"compose"}, {"compose", "render"}, {"compose", "render", "demo", "--unknown"},
		{"compose", "up", "demo"},
		{"compose", "down", "demo", "--volumes"},
		{"compose", "logs", "demo", "--follow"},
		{"compose", "health", "demo", "--json"},
		{"compose", "config", "demo", "--quiet"},
		{"compose", "rollback", "demo", "--yes"},
	} {
		var output bytes.Buffer
		if err := appCommand(args, paths, &output); err == nil {
			t.Errorf("appCommand(%v) unexpectedly succeeded", args)
		}
	}
}

func TestAppComposeActionsUseRenderedProjectAndSelectedContext(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")

	paths, composePath := testRenderedApp(t, "postgres")
	if err := saveSettings(paths.Config, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	servicesEnv := filepath.Join(paths.Config, "apps", "demo", "services.env")
	tests := []struct {
		action string
		want   []string
	}{
		{"up", []string{"--detach", "--no-build", "--remove-orphans", "--wait", "--wait-timeout", "300"}},
		{"down", []string{"--remove-orphans"}},
		{"config", []string{"--quiet"}},
		{"start", nil},
		{"stop", nil},
		{"restart", nil},
		{"ps", []string{"--all"}},
		{"logs", nil},
		{"exec", []string{"-T", "app", "sh"}},
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			var output bytes.Buffer
			extra := []string(nil)
			if tt.action == "exec" {
				extra = []string{"--"}
			}
			if err := appComposeOperation(tt.action, "demo", extra, paths, &output); err != nil {
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
			want := []string{"--context", "desktop-linux", "compose", "--env-file", servicesEnv, "--project-name", "dkit-demo", "--file", composePath, tt.action}
			want = append(want, tt.want...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Docker args = %#v, want %#v", got, want)
			}
			if tt.action == "up" && (!strings.Contains(output.String(), "passed their health checks") || !strings.Contains(output.String(), "not a blue/green traffic switch")) {
				t.Fatalf("up warning missing: %q", output.String())
			}
			if tt.action == "config" && !strings.Contains(output.String(), "No Docker state was changed") {
				t.Fatalf("config validation output missing: %q", output.String())
			}
		})
	}
}

func TestAppComposeHealthUsesSelectedContextAndReportsReadiness(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Health":"","Status":"Up"}
{"Name":"dkit-demo-postgres-1","Service":"postgres","State":"running","Health":"healthy","Status":"Up (healthy)"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	t.Setenv("DOCKER_CONTEXT", "")

	paths, composePath := testRenderedApp(t, "postgres")
	if err := saveSettings(paths.Config, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	servicesEnv := filepath.Join(paths.Config, "apps", "demo", "services.env")

	var output bytes.Buffer
	if err := appComposeOperation("health", "demo", nil, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "health-probe") || !strings.Contains(output.String(), "are ready") {
		t.Fatalf("health output = %q", output.String())
	}
	gotBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"--context", "desktop-linux", "compose", "--env-file", servicesEnv, "--project-name", "dkit-demo", "--file", composePath, "ps", "--all", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}

	output.Reset()
	if err := appComposeOperation("health", "demo", []string{"--json"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	var report appHealthReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("health JSON = %q: %v", output.String(), err)
	}
	if !report.Ready || report.App != "demo" || len(report.Containers) != 3 {
		t.Fatalf("health report = %#v", report)
	}

	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"unhealthy","Status":"Up (unhealthy)"}`)
	output.Reset()
	if err := appComposeOperation("health", "demo", []string{"--json"}, paths, &output); err == nil {
		t.Fatal("health check succeeded with an unhealthy HTTP probe")
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Ready || report.Error == "" {
		t.Fatalf("unhealthy health JSON = %q, report=%#v, err=%v", output.String(), report, err)
	}
}

func TestAppComposeApplyRendersCurrentConfigBeforeApplying(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, composePath := testRenderedApp(t, "postgres")
	if err := saveSettings(paths.Config, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.Update(paths.Config, "demo", map[string]string{"IMAGE": "example/demo:updated"}, nil); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := appComposeOperation("apply", "demo", nil, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Services may have been recreated") {
		t.Fatalf("apply output did not disclose effects: %q", output.String())
	}
	active, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), `image: "example/demo:updated"`) {
		t.Fatalf("apply did not render current app config: %s", active)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(args, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"--context", "desktop-linux", "compose", "--env-file", filepath.Join(paths.Config, "apps", "demo", "services.env"),
		"--project-name", "dkit-demo", "--file", composePath, "up", "--detach", "--no-build", "--remove-orphans", "--wait", "--wait-timeout", "300"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
}

func TestAppComposeApplyStopsWhenRenderFails(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "postgres")
	if err := os.Remove(filepath.Join(paths.Config, "apps", "demo", "app.env")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := appComposeOperation("apply", "demo", nil, paths, &output)
	if err == nil || !strings.Contains(err.Error(), "render app") {
		t.Fatalf("apply error = %v, want render failure", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("Docker ran despite render failure; capture stat error = %v", err)
	}
}

func TestParseComposeStatusesSupportsArrayAndRejectsMalformedOutput(t *testing.T) {
	statuses, err := parseComposeStatuses([]byte(`[{"Name":"app-1","Service":"app","State":"running","Status":"Up"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Service != "app" {
		t.Fatalf("parsed statuses = %#v", statuses)
	}
	if _, err := parseComposeStatuses([]byte("not json")); err == nil {
		t.Fatal("malformed Compose output was accepted")
	}
}

func TestAppComposeRollbackUsesPreviousVerifiedModelAndSelectedContext(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/old:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/new:2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(configDir, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/unrendered:3"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := appComposeProjectArgs("demo", paths); err == nil {
		t.Fatal("stale app.env was accepted for rollback")
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/new:2"}, nil); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	current, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(current), `image: "example/old:1"`) {
		t.Fatalf("rollback model does not contain prior image: %s", current)
	}
	if _, err := appComposeProjectArgs("demo", paths); err != nil {
		t.Fatalf("rolled-back model should remain usable: %v", err)
	}
	if !strings.Contains(output.String(), "does not switch public traffic") {
		t.Fatalf("rollback scope warning missing: %q", output.String())
	}
	manifestPath := filepath.Join(stateDir, "apps", "demo", "compose-history.json")
	manifestBeforeFailure, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(args, &got); err != nil {
		t.Fatal(err)
	}
	if !containsSequence(got, []string{"--context", "desktop-linux", "compose"}) ||
		!containsSequence(got, []string{"--file", filepath.Join(stateDir, "apps", "demo", "previous-compose.yaml"), "ps", "--all", "--format", "json"}) {
		t.Fatalf("rollback did not use selected context and prior model: %#v", got)
	}

	activeBeforeTamper, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composePath, []byte("tampered compose\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil {
		t.Fatal("rollback accepted a tampered active model")
	}
	if err := os.WriteFile(composePath, activeBeforeTamper, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"unhealthy","Status":"Up (unhealthy)"}`)
	output.Reset()
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil {
		t.Fatal("rollback succeeded despite unhealthy probe")
	}
	manifestAfterFailure, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfterFailure) != string(manifestBeforeFailure) {
		t.Fatalf("failed rollback changed history manifest: %s -> %s", manifestBeforeFailure, manifestAfterFailure)
	}
}

func TestAppComposeRollbackRestoresActiveModelAfterComposeFailure(t *testing.T) {
	capture := installDockerHelper(t)
	sequence := filepath.Join(filepath.Dir(capture), "docker-sequence.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_SEQUENCE", sequence)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/old:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/new:2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	activeBefore, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DKIT_TEST_DOCKER_FAIL_MATCH", "previous-compose.yaml up")

	err = appComposeOperation("rollback", "demo", nil, paths, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "re-applied the previously active model") {
		t.Fatalf("rollback error = %v, want recovered failure", err)
	}
	activeAfter, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(activeAfter) != string(activeBefore) {
		t.Fatalf("active model changed after failed rollback: %s -> %s", activeBefore, activeAfter)
	}

	lines, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	var invocations [][]string
	for _, line := range bytes.Split(bytes.TrimSpace(lines), []byte{'\n'}) {
		var args []string
		if err := json.Unmarshal(line, &args); err != nil {
			t.Fatal(err)
		}
		invocations = append(invocations, args)
	}
	foundRollback, foundRecovery, foundRecoveryCheck := false, false, false
	for _, args := range invocations {
		if containsSequence(args, []string{"--file", filepath.Join(stateDir, "apps", "demo", "previous-compose.yaml"), "up"}) {
			foundRollback = true
		}
		for _, arg := range args {
			if strings.Contains(arg, ".dkit-rollback-") {
				if containsSequence(args, []string{"up", "--detach"}) {
					foundRecovery = true
				}
				if containsSequence(args, []string{"ps", "--all", "--format", "json"}) {
					foundRecoveryCheck = true
				}
			}
		}
	}
	if !foundRollback || !foundRecovery || !foundRecoveryCheck {
		t.Fatalf("expected failed rollback and verified snapshot recovery, got %#v", invocations)
	}
}

func TestAppComposeExecPassesCommandAfterService(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, composePath := testRenderedApp(t, "postgres")
	servicesEnv := filepath.Join(paths.Config, "apps", "demo", "services.env")
	if err := appComposeOperation("exec", "demo", []string{"--", "php", "artisan", "about"}, paths, &bytes.Buffer{}); err != nil {
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
	want := []string{"--context", "default", "compose", "--env-file", servicesEnv, "--project-name", "dkit-demo", "--file", composePath, "exec", "-T", "app", "php", "artisan", "about"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker args = %#v, want %#v", got, want)
	}
}

func TestGeneratedComposeConfigUsesSelectedDockerContext(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")

	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(t.TempDir(), "settings")
	if err := saveSettings(settingsDir, settings{DockerContext: "desktop-linux"}); err != nil {
		t.Fatal(err)
	}
	args := []string{"-f", filepath.Join(stateDir, "apps", "demo", "compose.yaml"), "config"}
	if err := composeCommand(args, portable.Paths{Config: settingsDir}); err != nil {
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
	want := []string{"--context", "desktop-linux", "compose"}
	want = append(want, args...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Docker CLI arguments = %#v, want %#v", got, want)
	}
}
