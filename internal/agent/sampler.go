// Package agent implements the laptop agent: sampling, buffering, sync and
// the form state machine loop. OS-specific code sits behind Sampler.
package agent

import "time"

// Sample is one observation of the foreground app. It deliberately has no
// window title field: titles are never collected (privacy, PRD 8.3).
type Sample struct {
	App  string        // process executable name, e.g. "Code.exe" ("" if none)
	Idle time.Duration // time since last user input
}

// Sampler reads the current foreground app and idle time.
type Sampler interface {
	Sample() (Sample, error)
}

// SamplerFunc adapts a function to Sampler (used by tests).
type SamplerFunc func() (Sample, error)

// Sample implements Sampler.
func (f SamplerFunc) Sample() (Sample, error) { return f() }
