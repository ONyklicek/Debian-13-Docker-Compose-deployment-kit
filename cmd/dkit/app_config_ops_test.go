package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigOpsApp(t *testing.T, configDir, contents string) string {
	t.Helper()
	appDir := filepath.Join(configDir, "apps", "demo")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(appDir, "app.env")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDomainCommandAddsCanonicalUniqueDomainsAndLists(t *testing.T) {
	configDir := t.TempDir()
	path := writeConfigOpsApp(t, configDir, "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	var output bytes.Buffer
	if err := domainCommand([]string{"add", "demo", "WWW.Example.com", "www.example.com", "api.example.com"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "demo.example.com, www.example.com, api.example.com") {
		t.Fatalf("add output = %q", output.String())
	}
	output.Reset()
	if err := domainCommand([]string{"list", "demo"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "demo.example.com\nwww.example.com\napi.example.com\n" {
		t.Fatalf("domain list = %q", output.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "www.example.com") != 1 {
		t.Fatalf("domains were not canonicalized uniquely: %s", data)
	}
}

func TestDomainCommandRejectsInvalidDomainWithoutWriting(t *testing.T) {
	configDir := t.TempDir()
	path := writeConfigOpsApp(t, configDir, "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := domainCommand([]string{"add", "demo", "bad_domain"}, configDir, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid domain was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("invalid update changed app.env: %q -> %q", before, after)
	}
}

func TestWorkerCommandAddsListsAndRemovesWithoutDockerMutation(t *testing.T) {
	configDir := t.TempDir()
	path := writeConfigOpsApp(t, configDir, "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	var output bytes.Buffer
	if err := workerCommand([]string{"add", "demo", "queue", "--cmd", "php artisan queue:work", "--port", "9000", "--user", "www-data"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "no Docker operations") {
		t.Fatalf("worker add output = %q", output.String())
	}
	output.Reset()
	if err := workerCommand([]string{"list", "demo"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "queue\tphp artisan queue:work\n" {
		t.Fatalf("worker list = %q", output.String())
	}
	output.Reset()
	if err := workerCommand([]string{"remove", "demo", "queue"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "container is unchanged") {
		t.Fatalf("worker remove output = %q", output.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"WORKERS=queue", "WORKER_queue_CMD", "WORKER_queue_PORT", "WORKER_queue_USER"} {
		if strings.Contains(string(data), key) {
			t.Fatalf("removed worker key %q remains in app.env: %s", key, data)
		}
	}
}

func TestWorkerCommandValidatesValuesAndUnknownRemoval(t *testing.T) {
	configDir := t.TempDir()
	writeConfigOpsApp(t, configDir, "DOMAINS=demo.example.com\nSOURCE=image\nIMAGE=nginx:1.27\n")
	for _, args := range [][]string{
		{"add", "demo", "queue", "--cmd", "run", "--port", "invalid"},
		{"add", "demo", "queue", "--cmd", "run", "--path", "/public"},
		{"remove", "demo", "missing"},
		{"add", "demo", "bad_name", "--cmd", "run"},
	} {
		if err := workerCommand(args, configDir, &bytes.Buffer{}); err == nil {
			t.Errorf("workerCommand(%v) unexpectedly succeeded", args)
		}
	}
}
