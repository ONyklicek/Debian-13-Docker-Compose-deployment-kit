package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/deploymentstate"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

var errWriterClosed = errors.New("writer is closed")

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errWriterClosed
}

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

func TestAppComposeBlueGreenDeployAndRollback(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	sequence := filepath.Join(t.TempDir(), "docker-sequence.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_SEQUENCE", sequence)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	t.Setenv("DKIT_TEST_DOCKER_IMAGE_ID", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	var output bytes.Buffer
	if err := appComposeOperation("deploy", "demo", nil, paths, &output); err != nil {
		t.Fatal(err)
	}
	state, err := deploymentstate.Load(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	firstImage := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if state.ActiveSlot != "blue" || state.ActiveImage != firstImage || state.PreviousSlot != "" || state.DockerContext != "default" {
		t.Fatalf("first deployment state = %#v", state)
	}
	routePath := filepath.Join(stateDir, "traefik", "dynamic", "demo.yml")
	route, err := os.ReadFile(routePath)
	if err != nil || !strings.Contains(string(route), "http://dkit-demo-web-blue:8080") {
		t.Fatalf("first deployment route = %q, err=%v", route, err)
	}
	var healthOutput bytes.Buffer
	if err := appComposeOperation("health", "demo", []string{"--json"}, paths, &healthOutput); err != nil {
		t.Fatal(err)
	}
	var report appHealthReport
	if err := json.Unmarshal(healthOutput.Bytes(), &report); err != nil || !report.Ready || report.App != "demo (blue slot)" {
		t.Fatalf("blue slot health report = %#v, err=%v, output=%s", report, err, healthOutput.String())
	}
	if err := logsCommand([]string{"demo", "app"}, paths); err != nil {
		t.Fatal(err)
	}
	if err := execAppCommand([]string{"demo", "--", "sh", "-c", "true"}, paths); err != nil {
		t.Fatal(err)
	}
	execArgs, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var gotExec []string
	if err := json.Unmarshal(execArgs, &gotExec); err != nil {
		t.Fatal(err)
	}
	if !containsSequence(gotExec, []string{"exec", "-T", "-i", "web-blue"}) {
		t.Fatalf("exec did not target the active blue slot: %#v", gotExec)
	}
	model, err := os.ReadFile(filepath.Join(stateDir, "apps", "demo", "blue-green-compose.yaml"))
	if err != nil || !strings.Contains(string(model), "web-green") {
		t.Fatalf("blue/green Compose model = %q, err=%v", model, err)
	}

	secondImage := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	t.Setenv("DKIT_TEST_DOCKER_IMAGE_ID", secondImage)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-green-1","Service":"web-green","State":"running","Status":"Up"}
{"Name":"health-probe-green-1","Service":"health-probe-green","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/demo:2"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("deploy", "demo", nil, paths, &output); err != nil {
		t.Fatal(err)
	}
	state, err = deploymentstate.Load(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSlot != "green" || state.ActiveImage != secondImage || state.PreviousSlot != "blue" || state.PreviousImage != firstImage {
		t.Fatalf("second deployment state = %#v", state)
	}
	if state.ActiveInputsSHA256 == "" || state.ActiveInputsSHA256 != state.PreviousInputsSHA256 {
		t.Fatalf("deploy input checksums do not match unchanged environment files: %#v", state)
	}
	route, err = os.ReadFile(routePath)
	if err != nil || !strings.Contains(string(route), "http://dkit-demo-web-green:8080") {
		t.Fatalf("second deployment route = %q, err=%v", route, err)
	}

	runtimeEnvPath := filepath.Join(stateDir, "apps", "demo", "runtime.env")
	runtimeEnv, err := os.ReadFile(runtimeEnvPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeEnvPath, append(runtimeEnv, []byte("DRIFTED=1\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("config", "demo", nil, paths, &output); err == nil || !strings.Contains(err.Error(), "environment files have changed") {
		t.Fatalf("active-slot config accepted changed runtime environment: %v", err)
	}
	commandsAfterConfig, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	commandLines := strings.Split(strings.TrimSpace(string(commandsAfterConfig)), "\n")
	var lastCommand []string
	if err := json.Unmarshal([]byte(commandLines[len(commandLines)-1]), &lastCommand); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lastCommand, []string{"context", "show"}) {
		t.Fatalf("active-slot config ran Compose despite environment drift: %#v", lastCommand)
	}
	beforeDriftedRollback, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil || !strings.Contains(err.Error(), "runtime configuration or environment files have changed") {
		t.Fatalf("rollback accepted changed runtime environment: %v", err)
	}
	afterDriftedRollback, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterDriftedRollback, beforeDriftedRollback) {
		t.Fatalf("environment drift triggered a Docker operation: before=%s after=%s", beforeDriftedRollback, afterDriftedRollback)
	}
	if err := os.WriteFile(runtimeEnvPath, runtimeEnv, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_PATH", runtimeEnvPath)
	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_CONTENT", string(runtimeEnv)+"DRIFTED_DURING_ROLLBACK=1\n")
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil || !strings.Contains(err.Error(), "runtime configuration or environment files have changed") {
		t.Fatalf("rollback error = %v, want input drift failure before route switch", err)
	}
	state, err = deploymentstate.Load(stateDir, "demo")
	if err != nil || state.ActiveSlot != "green" {
		t.Fatalf("rollback input drift changed state = %#v, err=%v", state, err)
	}
	route, err = os.ReadFile(routePath)
	if err != nil || !strings.Contains(string(route), "http://dkit-demo-web-green:8080") {
		t.Fatalf("rollback input drift changed route = %q, err=%v", route, err)
	}
	if err := os.WriteFile(runtimeEnvPath, runtimeEnv, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_PATH", "")
	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_CONTENT", "")

	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"unhealthy","Status":"Up (unhealthy)"}`)
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil {
		t.Fatal("rollback succeeded with an unhealthy previous slot")
	}
	state, err = deploymentstate.Load(stateDir, "demo")
	if err != nil || state.ActiveSlot != "green" {
		t.Fatalf("unhealthy rollback changed state to %#v, err=%v", state, err)
	}
	route, err = os.ReadFile(routePath)
	if err != nil || !strings.Contains(string(route), "http://dkit-demo-web-green:8080") {
		t.Fatalf("unhealthy rollback changed route = %q, err=%v", route, err)
	}

	if _, err := appconfig.Update(configDir, "demo", map[string]string{
		"WORKERS":          "queue",
		"WORKER_queue_CMD": "run-queue",
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	beforeConfigDriftRollback, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err == nil || !strings.Contains(err.Error(), "environment files have changed") {
		t.Fatalf("rollback accepted changed app runtime configuration: %v", err)
	}
	afterConfigDriftRollback, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterConfigDriftRollback, beforeConfigDriftRollback) {
		t.Fatalf("app config drift triggered a Docker operation: before=%s after=%s", beforeConfigDriftRollback, afterConfigDriftRollback)
	}
	if _, err := appconfig.Update(configDir, "demo", nil, []string{"WORKERS", "WORKER_queue_CMD"}); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("rollback", "demo", nil, paths, &output); err != nil {
		t.Fatalf("rollback after restoring app runtime configuration failed: %v", err)
	}
	state, err = deploymentstate.Load(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveSlot != "blue" || state.ActiveImage != firstImage || state.PreviousSlot != "green" || state.PreviousImage != secondImage {
		t.Fatalf("rollback state = %#v", state)
	}
	route, err = os.ReadFile(routePath)
	if err != nil || !strings.Contains(string(route), "http://dkit-demo-web-blue:8080") {
		t.Fatalf("rollback route = %q, err=%v", route, err)
	}
	modelPath := filepath.Join(stateDir, "apps", "demo", "blue-green-compose.yaml")
	model, err = os.ReadFile(modelPath)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, append(model, []byte("# unexpected edit\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("config", "demo", nil, paths, &output); err == nil || !strings.Contains(err.Error(), "was modified since deployment") {
		t.Fatalf("config accepted edited deployment model: %v", err)
	}
	after, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var lastDockerArgs []string
	if err := json.Unmarshal(after, &lastDockerArgs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lastDockerArgs, []string{"context", "show"}) {
		t.Fatalf("edited deployment model triggered a Compose operation: before=%s after=%s", before, after)
	}
	if !strings.Contains(output.String(), "Previous slot") || !strings.Contains(output.String(), "Rolled app") {
		t.Fatalf("deployment output = %q", output.String())
	}
}

func TestBlueGreenCandidateRejectsMissingOrStaleGitBuild(t *testing.T) {
	tests := []struct {
		name        string
		build       string
		wantErrText string
	}{
		{
			name:        "missing build",
			wantErrText: "has no portable Git build",
		},
		{
			name: "docker context mismatch",
			build: `{"image":"example/demo:1","revision":"abc123",` +
				`"docker_context":"other","fingerprint":"unused"}`,
			wantErrText: "not selected context",
		},
		{
			name: "stale fingerprint",
			build: `{"image":"example/demo:1","revision":"abc123",` +
				`"docker_context":"default","fingerprint":"stale"}`,
			wantErrText: "build is stale",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "docker-args.json")
			installDockerHelper(t)
			t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
			t.Setenv("DOCKER_CONTEXT", "default")
			configDir, stateDir := t.TempDir(), t.TempDir()
			writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=git\nGIT_REPOSITORY=https://example.com/team/demo.git\n")
			paths := portable.Paths{Config: configDir, State: stateDir}
			if tt.build != "" {
				buildDir := filepath.Join(stateDir, "apps", "demo")
				if err := os.MkdirAll(buildDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(buildDir, "build.json"), []byte(tt.build), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := appComposeOperation("deploy", "demo", nil, paths, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErrText) {
				t.Fatalf("blue/green deploy error = %v, want text %q", err, tt.wantErrText)
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatalf("deploy performed a Docker operation before rejecting Git build metadata: stat err = %v", err)
			}
		})
	}
}

func TestBlueGreenInputChecksumCoversAllComposeEnvFiles(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	appPath := filepath.Join(configDir, "apps", "demo")
	statePath := filepath.Join(stateDir, "apps", "demo")
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir, State: stateDir}
	tests := []struct {
		name string
		path string
	}{
		{name: "app env", path: filepath.Join(appPath, "env")},
		{name: "runtime env", path: filepath.Join(statePath, "runtime.env")},
		{name: "services env", path: filepath.Join(appPath, "services.env")},
	}
	baseline, err := blueGreenInputsSHA256("demo", paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(tt.path, []byte("KEY=value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			changed, err := blueGreenInputsSHA256("demo", paths)
			if err != nil {
				t.Fatal(err)
			}
			if changed == baseline {
				t.Fatal("input checksum did not change when an env file was added")
			}
			if err := verifyBlueGreenInputs(paths, "demo", baseline, "blue"); err == nil || !strings.Contains(err.Error(), "runtime configuration or environment files have changed") {
				t.Fatalf("input verification accepted added env file: %v", err)
			}
			if err := os.WriteFile(tt.path, []byte("KEY=changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			changedContent, err := blueGreenInputsSHA256("demo", paths)
			if err != nil {
				t.Fatal(err)
			}
			if changedContent == changed {
				t.Fatal("input checksum did not change when env file content changed")
			}
			if err := verifyBlueGreenInputs(paths, "demo", baseline, "blue"); err == nil || !strings.Contains(err.Error(), "runtime configuration or environment files have changed") {
				t.Fatalf("input verification accepted changed env file content: %v", err)
			}
			if err := os.Remove(tt.path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBlueGreenInputChecksumCoversRuntimeConfigButIgnoresImageSelection(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	baseline, err := blueGreenInputsSHA256("demo", paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"IMAGE": "example/demo:2"}, nil); err != nil {
		t.Fatal(err)
	}
	imageChanged, err := blueGreenInputsSHA256("demo", paths)
	if err != nil {
		t.Fatal(err)
	}
	if imageChanged != baseline {
		t.Fatal("image selection change incorrectly invalidated previous runtime configuration")
	}
	if _, err := appconfig.Update(configDir, "demo", map[string]string{"DOMAINS": "new.example.com"}, nil); err != nil {
		t.Fatal(err)
	}
	domainChanged, err := blueGreenInputsSHA256("demo", paths)
	if err != nil {
		t.Fatal(err)
	}
	if domainChanged == baseline {
		t.Fatal("runtime configuration checksum did not change after domain update")
	}
}

func TestAppComposeBlueGreenDeployDoesNotSwitchRouteWhenCandidateUnhealthy(t *testing.T) {
	installDockerHelper(t)
	sequence := filepath.Join(t.TempDir(), "docker-sequence.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_SEQUENCE", sequence)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"unhealthy","Status":"Up (unhealthy)"}`)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	err := appComposeOperation("deploy", "demo", nil, paths, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not pass readiness") {
		t.Fatalf("deploy error = %v, want unhealthy candidate failure", err)
	}
	state, err := deploymentstate.Load(stateDir, "demo")
	if err != nil || state.ActiveSlot != "" {
		t.Fatalf("deployment state after failed candidate = %#v, err=%v", state, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "traefik", "dynamic", "demo.yml")); !os.IsNotExist(err) {
		t.Fatalf("unhealthy candidate got a public route: %v", err)
	}
	commands, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), `"stop"`) {
		t.Fatalf("failed candidate was not stopped: %s", commands)
	}
}

func TestAppComposeBlueGreenRejectsWorkersBeforeDockerOperations(t *testing.T) {
	installDockerHelper(t)
	sequence := filepath.Join(t.TempDir(), "docker-sequence.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_SEQUENCE", sequence)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\nWORKERS=queue\nWORKER_queue_CMD=run-queue\n")
	err := appComposeOperation("deploy", "demo", nil, portable.Paths{Config: configDir, State: stateDir}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "worker version coordination") {
		t.Fatalf("deploy error = %v, want explicit worker limitation", err)
	}
	commands, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commands), `"compose"`) || strings.Contains(string(commands), `"pull"`) || strings.Contains(string(commands), `"image"`) {
		t.Fatalf("deployment performed Docker mutations before rejecting unsupported workers: %s", commands)
	}
}

func TestAppComposeBlueGreenDeployDoesNotPromoteIfEnvChangesDuringUp(t *testing.T) {
	installDockerHelper(t)
	sequence := filepath.Join(t.TempDir(), "docker-sequence.jsonl")
	t.Setenv("DKIT_TEST_DOCKER_SEQUENCE", sequence)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"web-blue-1","Service":"web-blue","State":"running","Status":"Up"}
{"Name":"health-probe-blue-1","Service":"health-probe-blue","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	envPath := filepath.Join(configDir, "apps", "demo", "env")
	if err := os.WriteFile(envPath, []byte("APP_MODE=before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_PATH", envPath)
	t.Setenv("DKIT_TEST_DOCKER_MUTATE_ON_UP_CONTENT", "APP_MODE=changed-during-up\n")

	err := appComposeOperation("deploy", "demo", nil, paths, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "runtime configuration or environment files have changed") {
		t.Fatalf("deploy error = %v, want input drift failure", err)
	}
	state, err := deploymentstate.Load(stateDir, "demo")
	if err != nil || state.ActiveSlot != "" {
		t.Fatalf("state after input drift = %#v, err=%v", state, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "traefik", "dynamic", "demo.yml")); !os.IsNotExist(err) {
		t.Fatalf("route switched after input drift: %v", err)
	}
	commands, err := os.ReadFile(sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(commands), `"stop"`) {
		t.Fatalf("candidate slot was not stopped after input drift: %s", commands)
	}
}

func TestBlueGreenDeploymentBlocksSingleServiceMutationsAndRemoval(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	state := deploymentstate.State{
		ActiveSlot: "blue", ActiveImage: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ActiveRevision: "example/demo:1", DockerContext: "default", DeployedAt: "2026-10-21T00:00:00Z",
		ComposeSHA256: strings.Repeat("a", 64), ActiveInputsSHA256: strings.Repeat("b", 64),
	}
	if err := deploymentstate.Save(stateDir, "demo", state); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"apply", "up", "down", "start", "stop", "restart"} {
		if err := appComposeOperation(action, "demo", nil, paths, io.Discard); err == nil || !strings.Contains(err.Error(), "managed by blue/green slots") {
			t.Errorf("%s error = %v, want protected lifecycle rejection", action, err)
		}
	}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err == nil || !strings.Contains(err.Error(), "uses blue/green slots") {
		t.Errorf("render --write error = %v, want protected model rejection", err)
	}
	if err := removeAppCommand([]string{"demo", "--yes"}, paths, io.Discard); err == nil || !strings.Contains(err.Error(), "refusing removal") {
		t.Errorf("remove error = %v, want active slot protection", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("Docker ran during rejected single-service operations: %v", err)
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
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Health":"","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
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
			if tt.action == "start" || tt.action == "restart" {
				want = append(want[:len(want)-1], "ps", "--all", "--format", "json")
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Docker args = %#v, want %#v", got, want)
			}
			routePath := filepath.Join(paths.State, "traefik", "dynamic", "demo.yml")
			switch tt.action {
			case "up", "start", "restart":
				route, err := os.ReadFile(routePath)
				if err != nil || !strings.Contains(string(route), "http://dkit-demo-app:8080") {
					t.Fatalf("web route after %s = %q, err=%v", tt.action, route, err)
				}
			case "down", "stop":
				if _, err := os.Stat(routePath); !os.IsNotExist(err) {
					t.Fatalf("web route remained after %s: %v", tt.action, err)
				}
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

func TestAppComposeStartAndRestartDoNotEnableRouteUntilHTTPProbeIsHealthy(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Health":"","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"unhealthy","Status":"Up (unhealthy)"}`)
	t.Setenv("DOCKER_CONTEXT", "")
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=example/demo:latest\nHEALTH_TIMEOUT=1\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if err := appCommand([]string{"compose", "render", "demo", "--write"}, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "restart"} {
		err := appComposeOperation(action, "demo", nil, paths, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "public route was not changed") {
			t.Fatalf("%s error = %v, want health failure without route change", action, err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "traefik", "dynamic", "demo.yml")); !os.IsNotExist(err) {
			t.Fatalf("unhealthy app got a public route after %s: %v", action, err)
		}
	}
}

func TestAppComposeStopAndDownRestoreRouteWhenComposeFails(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "")
	if err := appComposeOperation("up", "demo", nil, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	routePath := filepath.Join(paths.State, "traefik", "dynamic", "demo.yml")
	original, err := os.ReadFile(routePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"stop", "down"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("DKIT_TEST_DOCKER_FAIL_MATCH", action)
			err := appComposeOperation(action, "demo", nil, paths, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "route was restored") {
				t.Fatalf("%s error = %v, want route restoration after failure", action, err)
			}
			restored, readErr := os.ReadFile(routePath)
			if readErr != nil || !bytes.Equal(restored, original) {
				t.Fatalf("route after failed %s = %q, err=%v", action, restored, readErr)
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
	if !strings.Contains(output.String(), "Services may have been recreated") ||
		!strings.Contains(output.String(), "Traefik web route was refreshed") {
		t.Fatalf("apply output did not disclose effects: %q", output.String())
	}
	routePath := filepath.Join(paths.State, "traefik", "dynamic", "demo.yml")
	if _, err := os.Stat(routePath); err != nil {
		t.Fatalf("apply did not write the public web route: %v", err)
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

func TestBlueGreenDeployRejectsComposeOverride(t *testing.T) {
	paths, _ := testRenderedApp(t, "")
	override := filepath.Join(paths.Config, "apps", "demo", "compose.override.yaml")
	if err := os.WriteFile(override, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := appComposeOperation("deploy", "demo", nil, paths, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "blue/green deploy does not support Compose overrides") {
		t.Fatalf("deploy error = %v, want explicit Compose override limitation", err)
	}
}

func TestComposeDeployRefRequiresGitSource(t *testing.T) {
	paths, _ := testRenderedApp(t, "")
	err := appComposeOperation("deploy", "demo", []string{"--ref", "release-1"}, paths, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "uses SOURCE=image") {
		t.Fatalf("deploy with ref error = %v, want image source rejection", err)
	}
}

func TestAppComposeApplyRemovesRouteWhenMadePrivate(t *testing.T) {
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DOCKER_CONTEXT", "")
	paths, _ := testRenderedApp(t, "")
	if err := appComposeOperation("up", "demo", nil, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	routePath := filepath.Join(paths.State, "traefik", "dynamic", "demo.yml")
	if _, err := os.Stat(routePath); err != nil {
		t.Fatalf("up did not create the public route: %v", err)
	}
	if _, err := appconfig.Update(paths.Config, "demo", map[string]string{"PUBLIC": "0"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := appComposeOperation("apply", "demo", nil, paths, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(routePath); !os.IsNotExist(err) {
		t.Fatalf("private app route remains after apply: %v", err)
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

func TestAppComposeRollbackKeepsLocalHistoryConsistentWhenHealthOutputFails(t *testing.T) {
	installDockerHelper(t)
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up"}
{"Name":"dkit-demo-health-probe-1","Service":"health-probe","State":"running","Health":"healthy","Status":"Up (healthy)"}`)
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

	err := appComposeOperation("rollback", "demo", nil, paths, failingWriter{})
	if !errors.Is(err, errWriterClosed) {
		t.Fatalf("rollback error = %v, want output error", err)
	}
	active, err := os.ReadFile(filepath.Join(stateDir, "apps", "demo", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(active), `image: "example/old:1"`) {
		t.Fatalf("rollback history was not promoted after healthy runtime rollback: %s", active)
	}
	previous, err := os.ReadFile(filepath.Join(stateDir, "apps", "demo", "previous-compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(previous), `image: "example/new:2"`) {
		t.Fatalf("previous model = %s, want former active model", previous)
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

func TestAppComposePSReportsContainerStatusAsJSON(t *testing.T) {
	configDir, stateDir := t.TempDir(), t.TempDir()
	writeAppConfig(t, configDir, "demo", "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	paths := portable.Paths{Config: configDir, State: stateDir}
	if _, err := writeAppComposeModel("demo", paths); err != nil {
		t.Fatal(err)
	}
	capture := installDockerHelper(t)
	t.Setenv("DKIT_TEST_DOCKER_CAPTURE", capture)
	t.Setenv("DKIT_TEST_DOCKER_COMPOSE_PS", `{"Name":"dkit-demo-app-1","Service":"app","State":"running","Status":"Up 5 minutes"}`)
	var output bytes.Buffer
	if err := appComposeOperation("ps", "demo", []string{"--json"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	var statuses []composeContainerStatus
	if err := json.Unmarshal(output.Bytes(), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Service != "app" || statuses[0].State != "running" {
		t.Fatalf("Compose status JSON = %#v", statuses)
	}
	argsData, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(argsData, &args); err != nil {
		t.Fatal(err)
	}
	if !containsSequence(args, []string{"ps", "--all", "--format", "json"}) {
		t.Fatalf("docker Compose ps args = %#v", args)
	}
}
