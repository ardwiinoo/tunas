// Package agentstats is the wire format of the laptop agent's own resource
// statistics (PRD Fase 8g). It has no dependencies, so the agent and the
// server share it, and it holds only numbers and a few fixed words: no free
// text, no window titles, no process list, no token.
package agentstats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// MaxSize is the largest accepted payload (PRD: a heartbeat stays under 1 KB).
const MaxSize = 1024

// Error kinds the agent may report (the kind of the last failure, never its text).
var errKinds = map[string]bool{"": true, "network": true, "timeout": true, "auth": true, "server": true, "other": true}

// Overlay states.
var overlayStates = map[string]bool{"": true, "off": true, "hidden": true, "visible": true}

// Stats is one sample of the agent process (only itself, never other processes).
type Stats struct {
	TS         int64   `json:"ts"`      // unix seconds of the sample
	Version    string  `json:"v"`       // agent build, e.g. "main-86a5e8c"
	UptimeS    int64   `json:"up"`      // seconds since the agent started
	PrivateMB  float64 `json:"priv_mb"` // private bytes: the "Memory" column of Task Manager
	PeakPrivMB float64 `json:"peak_mb"` // highest private bytes since the previous heartbeat
	WorkingMB  float64 `json:"ws_mb"`   // working set
	CPUPct     float64 `json:"cpu"`     // average since the previous sample, share of the whole machine
	Goroutines int     `json:"gor"`
	QueueAct   int     `json:"q_act"`  // activity buckets waiting to sync
	QueueWL    int     `json:"q_wl"`   // worklog entries waiting to sync
	SyncAgeS   int64   `json:"sync_s"` // seconds since the last successful sync (-1 = never)
	LastErr    string  `json:"err"`    // kind of the last failure: network|timeout|auth|server|other|""
	Overlay    string  `json:"ov"`     // off | hidden | visible
	Paused     bool    `json:"paused"`
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid agent stats")

var reVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// Validate checks a decoded sample.
func (s Stats) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	switch {
	case s.TS <= 0:
		return bad("ts")
	case s.Version != "" && !reVersion.MatchString(s.Version):
		return bad("version")
	case s.UptimeS < 0 || s.UptimeS > 10*365*86400:
		return bad("uptime")
	case !inRange(s.PrivateMB, 0, 1<<20) || !inRange(s.PeakPrivMB, 0, 1<<20) || !inRange(s.WorkingMB, 0, 1<<20):
		return bad("memory")
	case !inRange(s.CPUPct, 0, 100):
		return bad("cpu")
	case s.Goroutines < 0 || s.Goroutines > 1_000_000 || s.QueueAct < 0 || s.QueueAct > 100_000_000 || s.QueueWL < 0 || s.QueueWL > 1_000_000:
		return bad("counters")
	case s.SyncAgeS < -1:
		return bad("sync age")
	case !errKinds[s.LastErr]:
		return bad("error kind")
	case !overlayStates[s.Overlay]:
		return bad("overlay state")
	}
	return nil
}

func inRange(v, lo, hi float64) bool { return v >= lo && v <= hi && v == v } // v == v: not NaN

// Parse reads and validates a payload. Unknown fields are refused, so
// nothing but the fixed shape can pass through the server.
func Parse(raw []byte) (Stats, error) {
	if len(raw) == 0 || len(raw) > MaxSize {
		return Stats{}, fmt.Errorf("%w: size %d", ErrInvalid, len(raw))
	}
	var s Stats
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if dec.More() {
		return Stats{}, fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	return s, s.Validate()
}

// Marshal is the canonical JSON of a sample (what the server stores).
func (s Stats) Marshal() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}
