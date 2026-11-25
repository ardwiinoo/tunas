package agentstats

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func sample() Stats {
	return Stats{TS: 1_800_000_000, Version: "main-86a5e8c", UptimeS: 12345, PrivateMB: 54.2, PeakPrivMB: 58.9, WorkingMB: 28.6,
		CPUPct: 0.4, Goroutines: 31, QueueAct: 0, QueueWL: 2, SyncAgeS: 8, LastErr: "", Overlay: "hidden", Paused: false}
}

// The heartbeat must stay small: under 1 KB, and it carries no free text.
func TestPayloadIsSmallAndRoundTrips(t *testing.T) {
	raw, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 400 {
		t.Fatalf("a typical sample is %d bytes", len(raw))
	}
	worst := Stats{TS: 1_800_000_000, Version: strings.Repeat("v", 64), UptimeS: 9_999_999_999 % (10 * 365 * 86400), PrivateMB: 999999.123456, PeakPrivMB: 999999.123456,
		WorkingMB: 999999.123456, CPUPct: 99.999999, Goroutines: 999999, QueueAct: 99999999, QueueWL: 999999, SyncAgeS: 99999999, LastErr: "timeout", Overlay: "visible", Paused: true}
	if raw, err := worst.Marshal(); err != nil || len(raw) > MaxSize {
		t.Fatalf("worst case %d bytes (limit %d): %v", len(raw), MaxSize, err)
	}
	if g, err := Parse(mustMarshal(t, sample())); err != nil || g != sample() {
		t.Fatalf("round trip %+v %v", g, err)
	}
}

func mustMarshal(t *testing.T, s Stats) []byte {
	t.Helper()
	raw, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRejectsFreeTextAndExtras(t *testing.T) {
	good := string(mustMarshal(t, sample()))
	for name, in := range map[string]string{
		"window title":    strings.Replace(good, `"ov":"hidden"`, `"ov":"hidden","title":"rahasia.docx - Word"`, 1),
		"process list":    strings.Replace(good, `"gor":31`, `"gor":31,"procs":["chrome.exe","Code.exe"]`, 1),
		"token":           strings.Replace(good, `"gor":31`, `"gor":31,"token":"tnd_abc"`, 1),
		"free-text error": strings.Replace(good, `"err":""`, `"err":"dial tcp 10.0.0.5:443: connection refused"`, 1),
		"free-text ov":    strings.Replace(good, `"ov":"hidden"`, `"ov":"Spotify - lagu"`, 1),
		"bad version":     strings.Replace(good, `main-86a5e8c`, `main 86a5e8c; rm`, 1),
		"trailing":        good + `{"a":1}`,
		"not json":        `{rusak`,
		"empty":           ``,
		"too big":         `{"ts":1,"v":"` + strings.Repeat("a", MaxSize) + `"}`,
	} {
		if _, err := Parse([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestValidateRanges(t *testing.T) {
	cases := map[string]func(*Stats){
		"no ts":           func(s *Stats) { s.TS = 0 },
		"negative memory": func(s *Stats) { s.PrivateMB = -1 },
		"huge memory":     func(s *Stats) { s.WorkingMB = 1e12 },
		"NaN":             func(s *Stats) { s.CPUPct = math.NaN() },
		"cpu over 100":    func(s *Stats) { s.CPUPct = 101 },
		"negative queue":  func(s *Stats) { s.QueueWL = -3 },
		"bad sync age":    func(s *Stats) { s.SyncAgeS = -2 },
		"bad error kind":  func(s *Stats) { s.LastErr = "boom" },
		"negative uptime": func(s *Stats) { s.UptimeS = -1 },
	}
	for name, mutate := range cases {
		s := sample()
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, kind := range []string{"", "network", "timeout", "auth", "server", "other"} {
		s := sample()
		s.LastErr = kind
		if err := s.Validate(); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
	s := sample()
	s.SyncAgeS = -1 // never synced
	if err := s.Validate(); err != nil {
		t.Fatal("-1 means never")
	}
	if _, err := json.Marshal(sample()); err != nil {
		t.Fatal(err)
	}
}
