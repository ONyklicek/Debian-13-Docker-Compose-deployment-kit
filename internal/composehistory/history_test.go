package composehistory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestVerifyActiveRequiresConsistentManifestWhenModelMatchesRender(t *testing.T) {
	state := t.TempDir()
	model := []byte("services:\n  app:\n    image: example:current\n")
	if err := WriteRendered(state, "demo", model); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(state, "apps", "demo", "compose.yaml")
	metadata := filepath.Join(state, "apps", "demo", "compose-history.json")

	if !VerifyActive(state, "demo", model, model) {
		t.Fatal("valid rendered model and manifest were rejected")
	}
	validManifest, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(metadata); err != nil {
		t.Fatal(err)
	}
	if VerifyActive(state, "demo", model, model) {
		t.Fatal("model matching the render was accepted without its history manifest")
	}

	if err := WriteRendered(state, "demo", model); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadata, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if VerifyActive(state, "demo", model, model) {
		t.Fatal("model matching the render was accepted with a malformed history manifest")
	}

	if err := os.WriteFile(metadata, validManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	interruptedModel := []byte("services:\n  app:\n    image: example:interrupted\n")
	if err := os.WriteFile(active, interruptedModel, 0o600); err != nil {
		t.Fatal(err)
	}
	if VerifyActive(state, "demo", interruptedModel, interruptedModel) {
		t.Fatal("interrupted model write was accepted with the previous manifest")
	}
}

func TestPromotionRecoveryHandlesInterruptionAtEveryWriteBoundary(t *testing.T) {
	stages := []struct {
		name            string
		completedWrites int
	}{
		{name: "before-mutation", completedWrites: 0},
		{name: "after-previous-model", completedWrites: 1},
		{name: "after-active-model", completedWrites: 2},
		{name: "after-commit-manifest", completedWrites: 3},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			completedWrites := stage.completedWrites
			stateDir := t.TempDir()
			priorModel := []byte("services:\n  app:\n    image: example:prior\n")
			activeModel := []byte("services:\n  app:\n    image: example:active\n")
			if err := WriteRendered(stateDir, "demo", priorModel); err != nil {
				t.Fatal(err)
			}
			if err := WriteRendered(stateDir, "demo", activeModel); err != nil {
				t.Fatal(err)
			}
			activePath := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
			previousPath := filepath.Join(stateDir, "apps", "demo", "previous-compose.yaml")
			manifestPath := filepath.Join(stateDir, "apps", "demo", "compose-history.json")
			journalPath := manifestPath + ".transaction.json"
			activeBefore, err := os.ReadFile(activePath)
			if err != nil {
				t.Fatal(err)
			}
			previousBefore, err := os.ReadFile(previousPath)
			if err != nil {
				t.Fatal(err)
			}
			manifestBefore, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			var before manifest
			if err := json.Unmarshal(manifestBefore, &before); err != nil {
				t.Fatal(err)
			}
			after := before
			after.ActiveSHA256 = digest(previousBefore)
			after.PreviousSHA256 = digest(activeBefore)
			manifestAfter, err := manifestData(after)
			if err != nil {
				t.Fatal(err)
			}
			journal := promotionJournal{
				Version:              1,
				ActiveBefore:         activeBefore,
				PreviousBefore:       previousBefore,
				ManifestBefore:       manifestBefore,
				ActiveBeforeSHA256:   digest(activeBefore),
				PreviousBeforeSHA256: digest(previousBefore),
				ManifestBeforeSHA256: digest(manifestBefore),
				ActiveAfterSHA256:    digest(previousBefore),
				PreviousAfterSHA256:  digest(activeBefore),
				ManifestAfterSHA256:  digest(manifestAfter),
			}
			journalData, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if err := atomicWrite(journalPath, journalData); err != nil {
				t.Fatal(err)
			}
			if completedWrites >= 1 {
				if err := atomicWrite(previousPath, activeBefore); err != nil {
					t.Fatal(err)
				}
			}
			if completedWrites >= 2 {
				if err := atomicWrite(activePath, previousBefore); err != nil {
					t.Fatal(err)
				}
			}
			if completedWrites >= 3 {
				if err := atomicWrite(manifestPath, manifestAfter); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := PreviousModel(stateDir, "demo"); err != nil {
				t.Fatalf("recover history: %v", err)
			}
			gotActive, err := os.ReadFile(activePath)
			if err != nil {
				t.Fatal(err)
			}
			gotPrevious, err := os.ReadFile(previousPath)
			if err != nil {
				t.Fatal(err)
			}
			wantActive, wantPrevious := activeBefore, previousBefore
			if completedWrites == 3 {
				wantActive, wantPrevious = previousBefore, activeBefore
			}
			if string(gotActive) != string(wantActive) || string(gotPrevious) != string(wantPrevious) {
				t.Fatalf("recovered active/previous = %q/%q, want %q/%q",
					gotActive, gotPrevious, wantActive, wantPrevious)
			}
			if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
				t.Fatalf("transaction journal remains after recovery: %v", err)
			}
		})
	}
}

func TestPromotionRecoveryRejectsCorruptJournalWithoutChangingModels(t *testing.T) {
	stateDir := t.TempDir()
	priorModel := []byte("image: prior\n")
	activeModel := []byte("image: active\n")
	if err := WriteRendered(stateDir, "demo", priorModel); err != nil {
		t.Fatal(err)
	}
	if err := WriteRendered(stateDir, "demo", activeModel); err != nil {
		t.Fatal(err)
	}
	activePath := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	before, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(stateDir, "apps", "demo", "compose-history.json.transaction.json")
	journal := promotionJournal{Version: 1, ActiveBefore: []byte("tampered")}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviousModel(stateDir, "demo"); err == nil {
		t.Fatal("corrupt transaction journal was accepted")
	}
	after, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("active model changed during corrupt journal rejection: %q", after)
	}
}

func TestConcurrentRendersLeaveVerifiedHistory(t *testing.T) {
	stateDir := t.TempDir()
	const writers = 24
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wait sync.WaitGroup
	for i := range writers {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			model := []byte(fmt.Sprintf("services:\n  app:\n    image: example:%d\n", index))
			errs <- WriteRendered(stateDir, "demo", model)
		}(i)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	activePath := filepath.Join(stateDir, "apps", "demo", "compose.yaml")
	active, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyActive(stateDir, "demo", active, active) {
		t.Fatal("concurrent renders left active model inconsistent with its manifest")
	}
	previousPath, err := PreviousModel(stateDir, "demo")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous) == 0 || string(previous) == string(active) {
		t.Fatalf("previous model is empty or equal to active model: %q", previous)
	}
}

func TestComposeHistoryRejectsInvalidName(t *testing.T) {
	if err := WriteRendered(t.TempDir(), "../escape", []byte("invalid")); err == nil {
		t.Fatal("invalid app name accepted")
	}
}
