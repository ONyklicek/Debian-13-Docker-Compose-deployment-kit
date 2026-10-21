package composehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

type manifest struct {
	ActiveSHA256   string `json:"active_sha256"`
	PreviousSHA256 string `json:"previous_sha256,omitempty"`
	RenderSHA256   string `json:"render_sha256"`
}

type promotionJournal struct {
	Version              int    `json:"version"`
	ActiveBefore         []byte `json:"active_before"`
	PreviousBefore       []byte `json:"previous_before"`
	ManifestBefore       []byte `json:"manifest_before"`
	ActiveBeforeSHA256   string `json:"active_before_sha256"`
	PreviousBeforeSHA256 string `json:"previous_before_sha256"`
	ManifestBeforeSHA256 string `json:"manifest_before_sha256"`
	ActiveAfterSHA256    string `json:"active_after_sha256"`
	PreviousAfterSHA256  string `json:"previous_after_sha256"`
	ManifestAfterSHA256  string `json:"manifest_after_sha256"`
}

func paths(stateDir, name string) (string, string, string, error) {
	if stateDir == "" || !appconfig.ValidName(name) {
		return "", "", "", fmt.Errorf("invalid Compose history location")
	}
	dir := filepath.Join(stateDir, "apps", name)
	return filepath.Join(dir, "compose.yaml"), filepath.Join(dir, "previous-compose.yaml"),
		filepath.Join(dir, "compose-history.json"), nil
}

func WriteRendered(stateDir, name string, content []byte) error {
	return withHistoryLock(stateDir, name, func() error {
		return writeRendered(stateDir, name, content)
	})
}

func writeRendered(stateDir, name string, content []byte) error {
	active, previous, metadata, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		return fmt.Errorf("create app Compose history directory: %w", err)
	}
	if err := recoverPromotion(stateDir, name); err != nil {
		return fmt.Errorf("recover interrupted Compose rollback for app %q: %w", name, err)
	}
	var old []byte
	oldInfo, err := os.Lstat(active)
	if err == nil {
		if !oldInfo.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular Compose model %s", active)
		}
		old, err = os.ReadFile(active)
		if err != nil {
			return fmt.Errorf("read current Compose model %s: %w", active, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect current Compose model %s: %w", active, err)
	}

	var priorState manifest
	stateInfo, err := os.Lstat(metadata)
	if err == nil {
		if !stateInfo.Mode().IsRegular() {
			return fmt.Errorf("Compose history manifest %s is not a regular file", metadata)
		}
		stateData, err := os.ReadFile(metadata)
		if err != nil {
			return fmt.Errorf("read Compose history manifest %s: %w", metadata, err)
		}
		if err := json.Unmarshal(stateData, &priorState); err != nil {
			return fmt.Errorf("parse Compose history manifest %s: %w", metadata, err)
		}
		if priorState.ActiveSHA256 == "" || priorState.RenderSHA256 == "" {
			return fmt.Errorf("Compose history manifest %s is incomplete", metadata)
		}
		if len(old) > 0 && digest(old) != priorState.ActiveSHA256 {
			return fmt.Errorf("current Compose model for app %q was modified; refusing to replace it", name)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Compose history manifest %s: %w", metadata, err)
	}

	state := manifest{ActiveSHA256: digest(content), RenderSHA256: digest(content)}
	if len(old) > 0 && string(old) != string(content) && priorState.ActiveSHA256 != "" {
		if err := atomicWrite(previous, old); err != nil {
			return fmt.Errorf("save previous Compose model: %w", err)
		}
		state.PreviousSHA256 = digest(old)
	} else if priorState.PreviousSHA256 != "" {
		previousData, readErr := readRegular(previous)
		if readErr != nil {
			return fmt.Errorf("read previous Compose model: %w", readErr)
		}
		if digest(previousData) != priorState.PreviousSHA256 {
			return fmt.Errorf("previous Compose model for app %q was modified; refusing to replace it", name)
		}
		if string(old) == string(content) {
			state.PreviousSHA256 = priorState.PreviousSHA256
		}
	}
	if err := atomicWrite(active, content); err != nil {
		return err
	}
	return writeManifest(metadata, state)
}

func VerifyActive(stateDir, name string, rendered, current []byte) bool {
	valid := false
	err := withHistoryLock(stateDir, name, func() error {
		valid = verifyActive(stateDir, name, rendered, current)
		return nil
	})
	return err == nil && valid
}

func verifyActive(stateDir, name string, rendered, current []byte) bool {
	if err := recoverPromotion(stateDir, name); err != nil {
		return false
	}
	active, _, metadata, err := paths(stateDir, name)
	if err != nil {
		return false
	}
	info, err := os.Lstat(active)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := readRegular(metadata)
	if err != nil {
		return false
	}
	var state manifest
	if json.Unmarshal(data, &state) != nil || state.ActiveSHA256 == "" || state.RenderSHA256 != digest(rendered) {
		return false
	}
	return state.ActiveSHA256 == digest(current)
}

func PreviousModel(stateDir, name string) (string, error) {
	var model string
	err := withHistoryLock(stateDir, name, func() error {
		var err error
		model, err = previousModel(stateDir, name)
		return err
	})
	return model, err
}

func previousModel(stateDir, name string) (string, error) {
	if err := recoverPromotion(stateDir, name); err != nil {
		return "", fmt.Errorf("recover interrupted Compose rollback for app %q: %w", name, err)
	}
	_, previous, metadata, err := paths(stateDir, name)
	if err != nil {
		return "", err
	}
	data, err := readRegular(metadata)
	if err != nil {
		return "", fmt.Errorf("Compose rollback history for app %q is unavailable; render a changed model first: %w", name, err)
	}
	var state manifest
	if err := json.Unmarshal(data, &state); err != nil {
		return "", fmt.Errorf("parse Compose history for app %q: %w", name, err)
	}
	if state.ActiveSHA256 == "" || state.RenderSHA256 == "" {
		return "", fmt.Errorf("Compose history for app %q is incomplete", name)
	}
	if state.PreviousSHA256 == "" {
		return "", fmt.Errorf("app %q has no previous rendered Compose model", name)
	}
	model, err := readRegular(previous)
	if err != nil {
		return "", fmt.Errorf("read previous Compose model for app %q: %w", name, err)
	}
	if digest(model) != state.PreviousSHA256 {
		return "", fmt.Errorf("previous Compose model for app %q was modified; refusing rollback", name)
	}
	return previous, nil
}

func PromotePrevious(stateDir, name string) error {
	return withHistoryLock(stateDir, name, func() error {
		return promotePrevious(stateDir, name)
	})
}

func promotePrevious(stateDir, name string) error {
	if err := recoverPromotion(stateDir, name); err != nil {
		return fmt.Errorf("recover interrupted Compose rollback for app %q: %w", name, err)
	}
	active, previous, metadata, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	current, err := readRegular(active)
	if err != nil {
		return fmt.Errorf("read active Compose model for app %q: %w", name, err)
	}
	data, err := readRegular(metadata)
	if err != nil {
		return fmt.Errorf("read Compose history for app %q: %w", name, err)
	}
	var state manifest
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse Compose history for app %q: %w", name, err)
	}
	if state.ActiveSHA256 == "" || state.RenderSHA256 == "" {
		return fmt.Errorf("Compose history for app %q is incomplete", name)
	}
	if digest(current) != state.ActiveSHA256 {
		return fmt.Errorf("active Compose model for app %q was modified; refusing rollback", name)
	}
	prior, err := readRegular(previous)
	if err != nil {
		return fmt.Errorf("read previous Compose model for app %q: %w", name, err)
	}
	if state.PreviousSHA256 == "" || digest(prior) != state.PreviousSHA256 {
		return fmt.Errorf("previous Compose model for app %q is missing or modified; refusing rollback", name)
	}
	state.ActiveSHA256 = digest(prior)
	state.PreviousSHA256 = digest(current)
	// Keep the render hash for the current app.env configuration. This allows
	// only this recorded rollback model, not an unrelated stale file.
	manifestAfter, err := manifestData(state)
	if err != nil {
		return err
	}
	return promoteTransaction(stateDir, name, active, previous, metadata, current, prior, data, manifestAfter)
}

func withHistoryLock(stateDir, name string, operation func() error) (err error) {
	active, _, _, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(active)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create Compose history directory for app %q: %w", name, err)
	}
	lock := flock.New(filepath.Join(dir, ".compose-history.lock"))
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock Compose history for app %q: %w", name, err)
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock Compose history for app %q: %w", name, unlockErr))
		}
	}()
	return operation()
}

func readRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func writeManifest(path string, state manifest) error {
	data, err := manifestData(state)
	if err != nil {
		return err
	}
	return atomicWrite(path, data)
}

func manifestData(state manifest) ([]byte, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("encode Compose history: %w", err)
	}
	return append(data, '\n'), nil
}

func promoteTransaction(stateDir, name, active, previous, metadata string, activeBefore, previousBefore, manifestBefore, manifestAfter []byte) error {
	journalPath := metadata + ".transaction.json"
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
	data, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode Compose rollback transaction for app %q: %w", name, err)
	}
	if err := atomicWrite(journalPath, append(data, '\n')); err != nil {
		return fmt.Errorf("prepare Compose rollback transaction for app %q: %w", name, err)
	}
	if err := atomicWrite(previous, activeBefore); err != nil {
		return rollbackTransactionFailure(stateDir, name, fmt.Errorf("save active Compose model as recovery target: %w", err))
	}
	if err := atomicWrite(active, previousBefore); err != nil {
		return rollbackTransactionFailure(stateDir, name, fmt.Errorf("activate previous Compose model: %w", err))
	}
	if err := atomicWrite(metadata, manifestAfter); err != nil {
		return rollbackTransactionFailure(stateDir, name, fmt.Errorf("update Compose rollback manifest: %w", err))
	}
	if err := os.Remove(journalPath); err != nil {
		return fmt.Errorf("Compose rollback for app %q committed, but transaction journal %s could not be removed: %w", name, journalPath, err)
	}
	return nil
}

func rollbackTransactionFailure(stateDir, name string, cause error) error {
	if err := recoverPromotion(stateDir, name); err != nil {
		return fmt.Errorf("%v; recover Compose rollback transaction: %w", cause, err)
	}
	return cause
}

func recoverPromotion(stateDir, name string) error {
	_, _, metadata, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	journalPath := metadata + ".transaction.json"
	data, err := readRegular(journalPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read transaction journal %s: %w", journalPath, err)
	}
	var journal promotionJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("parse transaction journal %s: %w", journalPath, err)
	}
	if journal.Version != 1 || journal.ActiveBefore == nil || journal.PreviousBefore == nil ||
		journal.ManifestBefore == nil || journal.ActiveBeforeSHA256 != digest(journal.ActiveBefore) ||
		journal.PreviousBeforeSHA256 != digest(journal.PreviousBefore) ||
		journal.ManifestBeforeSHA256 != digest(journal.ManifestBefore) ||
		journal.ActiveAfterSHA256 == "" ||
		journal.PreviousAfterSHA256 == "" || journal.ManifestAfterSHA256 == "" {
		return fmt.Errorf("transaction journal %s is incomplete, corrupted, or unsupported", journalPath)
	}
	active, previous, _, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	activeNow, activeErr := readRegular(active)
	previousNow, previousErr := readRegular(previous)
	manifestNow, manifestErr := readRegular(metadata)
	if activeErr == nil && previousErr == nil && manifestErr == nil &&
		digest(activeNow) == journal.ActiveAfterSHA256 &&
		digest(previousNow) == journal.PreviousAfterSHA256 &&
		digest(manifestNow) == journal.ManifestAfterSHA256 {
		if err := os.Remove(journalPath); err != nil {
			return fmt.Errorf("remove committed transaction journal %s: %w", journalPath, err)
		}
		return nil
	}
	for _, check := range []struct {
		path string
		err  error
	}{{active, activeErr}, {previous, previousErr}, {metadata, manifestErr}} {
		if check.err != nil {
			return fmt.Errorf("inspect transaction file %s during recovery: %w", check.path, check.err)
		}
	}
	if err := atomicWrite(active, journal.ActiveBefore); err != nil {
		return fmt.Errorf("restore active Compose model during recovery: %w", err)
	}
	if err := atomicWrite(previous, journal.PreviousBefore); err != nil {
		return fmt.Errorf("restore previous Compose model during recovery: %w", err)
	}
	if err := atomicWrite(metadata, journal.ManifestBefore); err != nil {
		return fmt.Errorf("restore Compose history manifest during recovery: %w", err)
	}
	if err := os.Remove(journalPath); err != nil {
		return fmt.Errorf("remove recovered transaction journal %s: %w", journalPath, err)
	}
	return nil
}

func atomicWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".dkit-history-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.Write(content); err != nil {
		file.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("set permissions for %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync temporary file for %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync directory for %s: %w", path, err)
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
