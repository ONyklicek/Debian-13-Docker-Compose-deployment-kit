package composehistory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRenderedKeepsOneVerifiedPreviousModel(t *testing.T) {
	state := t.TempDir()
	oldModel := []byte("services:\n  app:\n    image: example:old\n")
	newModel := []byte("services:\n  app:\n    image: example:new\n")
	if err := WriteRendered(state, "demo", oldModel); err != nil {
		t.Fatal(err)
	}
	if err := WriteRendered(state, "demo", newModel); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(filepath.Join(state, "apps", "demo", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != string(newModel) || !VerifyActive(state, "demo", newModel, active) {
		t.Fatalf("active model = %q", active)
	}
	previous, err := PreviousModel(state, "demo")
	if err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal(err)
	}
	if string(prior) != string(oldModel) {
		t.Fatalf("previous model = %q, want %q", prior, oldModel)
	}
	if VerifyActive(state, "demo", oldModel, active) {
		t.Fatal("old app.env render was accepted while newer model is active")
	}
}

func TestPromotePreviousAndRejectTamperedHistory(t *testing.T) {
	state := t.TempDir()
	oldModel := []byte("image: old\n")
	newModel := []byte("image: new\n")
	if err := WriteRendered(state, "demo", oldModel); err != nil {
		t.Fatal(err)
	}
	if err := WriteRendered(state, "demo", newModel); err != nil {
		t.Fatal(err)
	}
	if err := PromotePrevious(state, "demo"); err != nil {
		t.Fatal(err)
	}
	active, err := os.ReadFile(filepath.Join(state, "apps", "demo", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(active) != string(oldModel) || !VerifyActive(state, "demo", newModel, active) {
		t.Fatalf("rollback active model = %q", active)
	}
	if VerifyActive(state, "demo", []byte("image: unrelated\n"), active) {
		t.Fatal("rollback model was accepted for unrelated app configuration")
	}
	previous, err := PreviousModel(state, "demo")
	if err != nil {
		t.Fatal(err)
	}
	rollForward, err := os.ReadFile(previous)
	if err != nil {
		t.Fatal(err)
	}
	if string(rollForward) != string(newModel) {
		t.Fatalf("previous model after rollback = %q, want former active model %q", rollForward, newModel)
	}
	if err := os.WriteFile(previous, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviousModel(state, "demo"); err == nil {
		t.Fatal("tampered previous model was accepted")
	}
}

func TestHistoryRejectsTamperedActiveModelAndCanRenderAfterRollback(t *testing.T) {
	state := t.TempDir()
	oldModel := []byte("image: old\n")
	newModel := []byte("image: new\n")
	if err := WriteRendered(state, "demo", oldModel); err != nil {
		t.Fatal(err)
	}
	if err := WriteRendered(state, "demo", newModel); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(state, "apps", "demo", "compose.yaml")
	if err := os.WriteFile(active, []byte("image: tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteRendered(state, "demo", []byte("image: next\n")); err == nil {
		t.Fatal("render replaced tampered active model")
	}
	if err := os.WriteFile(active, newModel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PromotePrevious(state, "demo"); err != nil {
		t.Fatal(err)
	}
	nextModel := []byte("image: after-rollback-render\n")
	if err := WriteRendered(state, "demo", nextModel); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(active)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(nextModel) {
		t.Fatalf("active model after render = %q", got)
	}
	previousPath, err := PreviousModel(state, "demo")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(previous) != string(oldModel) {
		t.Fatalf("previous model after render = %q, want %q", previous, oldModel)
	}
}

func TestComposeHistoryRejectsInvalidName(t *testing.T) {
	if err := WriteRendered(t.TempDir(), "../escape", []byte("invalid")); err == nil {
		t.Fatal("invalid app name accepted")
	}
}
