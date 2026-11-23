package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// UIState is %APPDATA%\tunas\state.json: the tray switches. It is kept
// apart from config.yaml so the owner's comments there are never rewritten.
type UIState struct {
	TrackingPaused   bool `json:"tracking_paused"`
	WorklogBubbles   bool `json:"worklog_bubbles"`
	CharacterVisible bool `json:"character_visible"`
	AutostartOffered bool `json:"autostart_offered"`
}

// DefaultUIState is used when there is no state.json yet.
func DefaultUIState() UIState { return UIState{WorklogBubbles: true, CharacterVisible: true} }

// LoadUIState reads state.json; a missing or broken file gives defaults.
func LoadUIState(path string) UIState {
	st := DefaultUIState()
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	if json.Unmarshal(b, &st) != nil {
		return DefaultUIState()
	}
	return st
}

// SaveUIState writes state.json atomically (temp file + rename), so a
// crash never leaves half a file.
func SaveUIState(path string, st UIState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// RunKey is the per-user autostart key; AutostartName its value name.
const (
	RunKey        = `Software\Microsoft\Windows\CurrentVersion\Run`
	AutostartName = "Tunas"
)

// Registry is the autostart value store (the Windows registry, or a fake
// in tests).
type Registry interface {
	Get(name string) (value string, ok bool, err error)
	Set(name, value string) error
	Delete(name string) error
}

// AutostartCommand is the Run value for exe: quoted path + --background.
func AutostartCommand(exe string) string { return `"` + exe + `" --background` }

// AutostartStatus reads the registry (the source of truth): enabled, and
// whether the stored path points at another exe (moved: offer to update).
func AutostartStatus(r Registry, exe string) (enabled, stale bool, err error) {
	v, ok, err := r.Get(AutostartName)
	if err != nil || !ok {
		return false, false, err
	}
	return true, !strings.EqualFold(strings.TrimSpace(v), AutostartCommand(exe)), nil
}

// SetAutostart turns autostart on (writing the current exe) or off.
func SetAutostart(r Registry, exe string, on bool) error {
	if on {
		return r.Set(AutostartName, AutostartCommand(exe))
	}
	err := r.Delete(AutostartName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
