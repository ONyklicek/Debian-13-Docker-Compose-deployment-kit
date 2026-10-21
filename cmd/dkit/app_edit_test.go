package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestAppGetSetAndEnvCommands(t *testing.T) {
	configDir := t.TempDir()
	appEnv := writeAppConfig(t, configDir, "shop", appConfigFixture)
	if err := os.WriteFile(filepath.Join(filepath.Dir(appEnv), "env"), []byte("APP_NAME=shop\nDB_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := portable.Paths{Config: configDir}

	var output bytes.Buffer
	if err := appCommand([]string{"get", "shop", "WEB_PORT"}, paths, &output); err != nil || strings.TrimSpace(output.String()) != "8080" {
		t.Fatalf("app get output %q, error %v", output.String(), err)
	}
	output.Reset()
	if err := appCommand([]string{"set", "shop", "WEB_PORT=3000", "--unset", "HEALTH_PATH"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No Docker operations") {
		t.Fatalf("app set output = %q", output.String())
	}
	output.Reset()
	if err := appCommand([]string{"get", "shop", "--json"}, paths, &output); err != nil {
		t.Fatal(err)
	}
	var appValues map[string]string
	if err := json.Unmarshal(output.Bytes(), &appValues); err != nil {
		t.Fatal(err)
	}
	if appValues["WEB_PORT"] != "3000" {
		t.Fatalf("updated WEB_PORT = %q", appValues["WEB_PORT"])
	}
	if _, exists := appValues["HEALTH_PATH"]; exists {
		t.Fatal("unset HEALTH_PATH still present")
	}

	output.Reset()
	if err := envCommand([]string{"list", "shop"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret") || !strings.Contains(output.String(), "DB_PASSWORD=•••••• (--show)") {
		t.Fatalf("env list did not mask secret: %q", output.String())
	}
	output.Reset()
	if err := envCommand([]string{"set", "shop", "APP_NAME=new app", "API_TOKEN=a$secret"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := envCommand([]string{"list", "shop", "--show"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "APP_NAME=new app") || !strings.Contains(output.String(), "API_TOKEN=a$secret") {
		t.Fatalf("env show output = %q", output.String())
	}
	output.Reset()
	if err := envCommand([]string{"unset", "shop", "API_TOKEN"}, configDir, &output); err != nil {
		t.Fatal(err)
	}
	values, err := os.ReadFile(filepath.Join(filepath.Dir(appEnv), "env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(values), "API_TOKEN") {
		t.Fatalf("unset did not remove key: %q", values)
	}
}

func TestConfigEditRejectsInvalidResultsAndMalformedArguments(t *testing.T) {
	configDir := t.TempDir()
	path := writeAppConfig(t, configDir, "shop", appConfigFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := appCommand([]string{"set", "shop", "DOMAINS=invalid"}, portable.Paths{Config: configDir}, &output); err == nil {
		t.Fatal("app set accepted an invalid configuration")
	}
	if err := envCommand([]string{"set", "shop", "BAD-KEY=x"}, configDir, &output); err == nil {
		t.Fatal("env set accepted an invalid key")
	}
	for _, args := range [][]string{
		{"get"}, {"get", "shop", "--other"}, {"set", "shop"}, {"set", "shop", "bad"},
	} {
		if err := appCommand(args, portable.Paths{Config: configDir}, &output); err == nil {
			t.Errorf("appCommand(%v) unexpectedly succeeded", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid app set changed app.env")
	}
}
