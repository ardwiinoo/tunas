//go:build !windows

package secret

import "errors"

type unsupported struct{}

func (unsupported) Protect([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI exists only on Windows")
}
func (unsupported) Unprotect([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI exists only on Windows")
}

// Default is the protector the agent uses (the agent only runs on Windows;
// elsewhere only tests with a fake Protector reach this package).
func Default() Protector { return unsupported{} }
