package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCollectStatusUsesSelectedContextAndParsesResults(t *testing.T) {
	var calls [][]string
	status, err := collectStatus("desktop-linux", func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch {
		case reflect.DeepEqual(args, []string{"--context", "desktop-linux", "info", "--format", "{{json .}}"}):
			return []byte(`{"Name":"desktop","ServerVersion":"28.0.0","OperatingSystem":"Docker Desktop","OSType":"linux","Architecture":"aarch64","Containers":2,"ContainersRunning":1,"ContainersPaused":0,"ContainersStopped":1}`), nil
		case reflect.DeepEqual(args, []string{"--context", "desktop-linux", "ps", "-a", "--format", "{{json .}}"}):
			return []byte("{\"ID\":\"abc123\",\"Names\":\"web\",\"Image\":\"nginx:alpine\",\"State\":\"running\",\"Status\":\"Up 2 minutes\"}\n" +
				"{\"ID\":\"def456\",\"Names\":\"db\",\"Image\":\"postgres:17\",\"State\":\"exited\",\"Status\":\"Exited (0)\"}\n"), nil
		default:
			return nil, errors.New("unexpected Docker arguments")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("Docker calls = %d, want 2", len(calls))
	}
	if status.Name != "desktop" || status.ServerVersion != "28.0.0" ||
		status.OperatingSystem != "Docker Desktop" || status.OSType != "linux" ||
		status.Architecture != "aarch64" || status.Containers != 2 ||
		status.ContainersRunning != 1 || status.ContainersStopped != 1 {
		t.Fatalf("unexpected daemon status: %#v", status)
	}
	wantContainers := []containerStatus{
		{ID: "abc123", Names: "web", Image: "nginx:alpine", State: "running", Status: "Up 2 minutes"},
		{ID: "def456", Names: "db", Image: "postgres:17", State: "exited", Status: "Exited (0)"},
	}
	if !reflect.DeepEqual(status.ContainerList, wantContainers) {
		t.Fatalf("containers = %#v, want %#v", status.ContainerList, wantContainers)
	}
}

func TestCollectStatusReportsUnavailableDaemon(t *testing.T) {
	_, err := collectStatus("remote-prod", func(args ...string) ([]byte, error) {
		return nil, errors.New("connection refused")
	})
	if err == nil || !strings.Contains(err.Error(), "read Docker daemon info") ||
		!strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("collectStatus error = %v, want explicit daemon connection error", err)
	}
}

func TestCollectStatusRejectsMalformedDaemonAndContainerOutput(t *testing.T) {
	tests := []struct {
		name    string
		outputs [][]byte
		wantErr string
	}{
		{name: "info", outputs: [][]byte{[]byte("{")}, wantErr: "parse Docker daemon info"},
		{name: "containers", outputs: [][]byte{
			[]byte(`{"Name":"docker"}`), []byte("{bad json}\n"),
		}, wantErr: "parse Docker container list line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := 0
			_, err := collectStatus("", func(args ...string) ([]byte, error) {
				if index >= len(tt.outputs) {
					t.Fatalf("unexpected Docker call: %v", args)
				}
				out := tt.outputs[index]
				index++
				return out, nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("collectStatus error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestPrintStatus(t *testing.T) {
	var output bytes.Buffer
	err := printStatus(&output, daemonStatus{
		Context: "desktop-linux", Name: "desktop", ServerVersion: "28.0.0",
		OperatingSystem: "Docker Desktop", OSType: "linux", Architecture: "aarch64",
		Containers: 1, ContainersRunning: 1,
		ContainerList: []containerStatus{{ID: "abc123", Names: "web", Image: "nginx:alpine", State: "running", Status: "Up 2 minutes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"desktop-linux", "28.0.0", "1 total, 1 running", "abc123", "nginx:alpine", "Up 2 minutes"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("status output %q does not contain %q", output.String(), want)
		}
	}
}
