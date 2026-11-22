//go:build !windows

package agent

// SingleInstance is a no-op outside Windows (the agent only runs there).
func SingleInstance(string) (func(), error) { return func() {}, nil }

// OpenConsole is a no-op outside Windows: stderr is already a terminal.
func OpenConsole(string) (bool, error) { return false, nil }
