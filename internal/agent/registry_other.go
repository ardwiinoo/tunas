//go:build !windows

package agent

import "errors"

// UserRunKey is unavailable outside Windows.
type UserRunKey struct{}

var errNoRegistry = errors.New("autostart is only supported on Windows")

// Get reports no value.
func (UserRunKey) Get(string) (string, bool, error) { return "", false, nil }

// Set fails.
func (UserRunKey) Set(string, string) error { return errNoRegistry }

// Delete fails.
func (UserRunKey) Delete(string) error { return errNoRegistry }
