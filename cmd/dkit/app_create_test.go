package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestAppCreateWritesAndValidatesConfig(t *testing.T) {
	configDir := t.TempDir()
	var output bytes.Buffer
	err := appCommand([]string{
		"create", "kuma", "--preset", "image", "--source", "image", "--domain", "status.example.com",
		"--image", "ghcr.io/example/status:1", "--port", "3001",
	}, portable.Paths{Config: configDir}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No Docker operations were performed") {
		t.Fatalf("create output = %q", output.String())
	}
	app, err := appconfig.Read(configDir, "kuma")
	if err != nil || !app.Valid {
		t.Fatalf("created app check = valid %v, errors %v, read error %v", app.Valid, app.Errors, err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "apps", "kuma", "ssh")); err != nil {
		t.Fatalf("SSH directory missing: %v", err)
	}
}

func TestCreateAppRejectsIncompleteAndUnknownOptions(t *testing.T) {
	tests := [][]string{
		nil,
		{"shop", "--preset"},
		{"shop", "--preset", "dockerfile", "--source", "image", "--domain", "shop.example.com", "--repo", "https://example.com/shop.git"},
		{"shop", "--preset", "dockerfile", "--domain", "shop.example.com", "--repo", "https://example.com/shop.git", "--unknown"},
		{"shop", "--preset", "dockerfile", "--domain", "shop.example.com", "--repo"},
	}
	for _, args := range tests {
		var output bytes.Buffer
		if err := createApp(args, t.TempDir(), &output); err == nil {
			t.Errorf("createApp(%v) unexpectedly succeeded", args)
		}
	}
}
