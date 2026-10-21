package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/portable"
)

func TestAppCloneCommandIsPortableAndNonDeploying(t *testing.T) {
	configDir := t.TempDir()
	source := writeAppConfig(t, configDir, "source", "DOMAINS=old.example.com\nSOURCE=image\nIMAGE=example/app:latest\n")
	if err := os.WriteFile(filepath.Join(filepath.Dir(source), "env"), []byte("APP_KEY=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := appCommand([]string{"clone", "source", "copy", "--domain", "new.example.com", "--domain", "www.new.example.com"}, portable.Paths{Config: configDir}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No Docker operations") || !strings.Contains(output.String(), "including secrets") {
		t.Fatalf("clone output does not describe scope and copied secrets: %q", output.String())
	}
	if err := appCommand([]string{"clone", "source", "copy-no-env", "--domain", "other.example.com", "--no-env"}, portable.Paths{Config: configDir}, &output); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"clone", "source"},
		{"clone", "source", "bad", "--domain"},
		{"clone", "source", "bad", "--unknown"},
	} {
		if err := appCommand(args, portable.Paths{Config: configDir}, &output); err == nil {
			t.Errorf("appCommand(%v) unexpectedly succeeded", args)
		}
	}
}
