//go:build windows

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/windows/registry"
)

// UserRunKey is HKCU\...\Run (no admin rights needed).
type UserRunKey struct{}

// Get reads a string value.
func (UserRunKey) Get(name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, RunKey, registry.QUERY_VALUE)
	if err != nil {
		return "", false, err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return v, err == nil, err
}

// Set writes a string value.
func (UserRunKey) Set(name, value string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, RunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

// Delete removes a value (os.ErrNotExist if there is none).
func (UserRunKey) Delete(name string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, RunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(name); errors.Is(err, registry.ErrNotExist) {
		return os.ErrNotExist
	} else {
		return err
	}
}
