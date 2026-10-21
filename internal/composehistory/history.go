package composehistory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

type manifest struct {
	ActiveSHA256   string `json:"active_sha256"`
	PreviousSHA256 string `json:"previous_sha256,omitempty"`
	RenderSHA256   string `json:"render_sha256"`
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
	active, previous, metadata, err := paths(stateDir, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(active), 0o700); err != nil {
		return fmt.Errorf("create app Compose history directory: %w", err)
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
	if string(rendered) == string(current) {
		return true
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
	if err := atomicWrite(previous, current); err != nil {
		return fmt.Errorf("save active Compose model as recovery target: %w", err)
	}
	if err := atomicWrite(active, prior); err != nil {
		if restoreErr := atomicWrite(previous, prior); restoreErr != nil {
			return fmt.Errorf("activate previous Compose model: %v; restore previous history: %w", err, restoreErr)
		}
		return err
	}
	oldState := state
	state.ActiveSHA256 = digest(prior)
	state.PreviousSHA256 = digest(current)
	// Keep the render hash for the current app.env configuration. This allows
	// only this recorded rollback model, not an unrelated stale file.
	if err := writeManifest(metadata, state); err != nil {
		restoreActiveErr := atomicWrite(active, current)
		restorePreviousErr := atomicWrite(previous, prior)
		restoreManifestErr := writeManifest(metadata, oldState)
		if restoreActiveErr != nil || restorePreviousErr != nil || restoreManifestErr != nil {
			return fmt.Errorf("promote Compose rollback history: %v; recovery failures (active=%v, previous=%v, manifest=%v)",
				err, restoreActiveErr, restorePreviousErr, restoreManifestErr)
		}
		return fmt.Errorf("promote Compose rollback history: %w", err)
	}
	return nil
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
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode Compose history: %w", err)
	}
	return atomicWrite(path, append(data, '\n'))
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
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("set permissions for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
