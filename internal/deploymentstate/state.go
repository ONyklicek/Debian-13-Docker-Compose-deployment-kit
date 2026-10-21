package deploymentstate

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/onyklicek/debian-13-docker-compose-deployment-kit/internal/appconfig"
)

type State struct {
	ActiveSlot           string `json:"active_slot,omitempty"`
	PreviousSlot         string `json:"previous_slot,omitempty"`
	ActiveImage          string `json:"active_image,omitempty"`
	PreviousImage        string `json:"previous_image,omitempty"`
	ActiveRevision       string `json:"active_revision,omitempty"`
	PrevRevision         string `json:"previous_revision,omitempty"`
	DockerContext        string `json:"docker_context,omitempty"`
	DeployedAt           string `json:"deployed_at,omitempty"`
	ComposeSHA256        string `json:"compose_sha256,omitempty"`
	ActiveInputsSHA256   string `json:"active_inputs_sha256,omitempty"`
	PreviousInputsSHA256 string `json:"previous_inputs_sha256,omitempty"`
}

func Load(stateDir, name string) (State, error) {
	path, err := statePath(stateDir, name)
	if err != nil {
		return State{}, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("inspect deployment state for app %q: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return State{}, fmt.Errorf("deployment state for app %q is not a regular file", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("read deployment state for app %q: %w", name, err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parse deployment state for app %q: %w", name, err)
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("deployment state for app %q is invalid: %w", name, err)
	}
	return state, nil
}

func Save(stateDir, name string, state State) error {
	path, err := statePath(stateDir, name)
	if err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("refuse invalid deployment state for app %q: %w", name, err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode deployment state for app %q: %w", name, err)
	}
	data = append(data, '\n')
	if err := atomicWrite(path, data); err != nil {
		return fmt.Errorf("write deployment state for app %q: %w", name, err)
	}
	return nil
}

func statePath(stateDir, name string) (string, error) {
	if stateDir == "" || !appconfig.ValidName(name) {
		return "", fmt.Errorf("invalid app deployment state location")
	}
	return filepath.Join(stateDir, "apps", name, "deployment.json"), nil
}

func (state State) Validate() error {
	if state.ActiveSlot == "" {
		if state.PreviousSlot != "" || state.ActiveImage != "" || state.PreviousImage != "" ||
			state.ActiveRevision != "" || state.PrevRevision != "" || state.DockerContext != "" ||
			state.DeployedAt != "" || state.ComposeSHA256 != "" || state.ActiveInputsSHA256 != "" ||
			state.PreviousInputsSHA256 != "" {
			return fmt.Errorf("deployment metadata exists without an active slot")
		}
		return nil
	}
	if state.ActiveSlot != "blue" && state.ActiveSlot != "green" {
		return fmt.Errorf("active slot must be blue or green")
	}
	if state.ActiveImage == "" || strings.TrimSpace(state.ActiveImage) != state.ActiveImage {
		return fmt.Errorf("active image is missing or malformed")
	}
	if len(state.ComposeSHA256) != 64 {
		return fmt.Errorf("blue/green Compose model checksum is missing or malformed")
	}
	if _, err := hex.DecodeString(state.ComposeSHA256); err != nil || strings.ToLower(state.ComposeSHA256) != state.ComposeSHA256 {
		return fmt.Errorf("blue/green Compose model checksum is malformed")
	}
	if !validSHA256(state.ActiveInputsSHA256) {
		return fmt.Errorf("active Compose environment checksum is missing or malformed")
	}
	if state.PreviousSlot == "" {
		if state.PreviousImage != "" || state.PrevRevision != "" || state.PreviousInputsSHA256 != "" {
			return fmt.Errorf("previous deployment metadata exists without a previous slot")
		}
		return nil
	}
	if (state.PreviousSlot != "blue" && state.PreviousSlot != "green") || state.PreviousSlot == state.ActiveSlot {
		return fmt.Errorf("previous slot must be the other blue/green slot")
	}
	if state.PreviousImage == "" || strings.TrimSpace(state.PreviousImage) != state.PreviousImage {
		return fmt.Errorf("previous image is missing or malformed")
	}
	if !validSHA256(state.PreviousInputsSHA256) {
		return fmt.Errorf("previous Compose environment checksum is missing or malformed")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func atomicWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dkit-deployment-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
