package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/forms"
)

func TestUIStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if st := LoadUIState(path); st != DefaultUIState() || !st.WorklogBubbles || !st.CharacterVisible {
		t.Fatalf("defaults %+v", st)
	}
	want := UIState{TrackingPaused: true, WorklogBubbles: false, CharacterVisible: true, AutostartOffered: true}
	if err := SaveUIState(path, want); err != nil {
		t.Fatal(err)
	}
	if got := LoadUIState(path); got != want {
		t.Fatalf("round trip %+v", got)
	}
	// atomic: no temp files left behind
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	if err := os.WriteFile(path, []byte("{rusak"), 0o600); err != nil {
		t.Fatal(err)
	}
	if LoadUIState(path) != DefaultUIState() {
		t.Fatal("a broken file falls back to defaults")
	}
}

// fakeRegistry stands in for HKCU\...\Run.
type fakeRegistry struct{ values map[string]string }

func (f *fakeRegistry) Get(name string) (string, bool, error) {
	v, ok := f.values[name]
	return v, ok, nil
}
func (f *fakeRegistry) Set(name, value string) error { f.values[name] = value; return nil }
func (f *fakeRegistry) Delete(name string) error {
	if _, ok := f.values[name]; !ok {
		return os.ErrNotExist
	}
	delete(f.values, name)
	return nil
}

func TestAutostartRegistry(t *testing.T) {
	r := &fakeRegistry{values: map[string]string{}}
	exe := `C:\Users\x\Tunas\tunas-agent.exe`
	if on, stale, err := AutostartStatus(r, exe); on || stale || err != nil {
		t.Fatal("off by default")
	}
	if err := SetAutostart(r, exe, true); err != nil {
		t.Fatal(err)
	}
	if r.values[AutostartName] != `"C:\Users\x\Tunas\tunas-agent.exe" --background` {
		t.Fatalf("value %q", r.values[AutostartName])
	}
	if on, stale, _ := AutostartStatus(r, exe); !on || stale {
		t.Fatal("on, same path")
	}
	if on, stale, _ := AutostartStatus(r, `D:\baru\tunas-agent.exe`); !on || !stale {
		t.Fatal("the exe moved: offer to update the path")
	}
	if err := SetAutostart(r, exe, false); err != nil || len(r.values) != 0 {
		t.Fatal("off removes the value")
	}
	if err := SetAutostart(r, exe, false); err != nil {
		t.Fatal("turning off twice is fine")
	}
}

func TestShowWorklogBubble(t *testing.T) {
	due := WorklogEvent{Type: "worklog.due", PromptID: 1}
	cases := []struct {
		e       WorklogEvent
		idle    time.Duration
		form    forms.Form
		inFocus bool
		want    bool
	}{
		{due, 0, forms.Biasa, false, true},
		{due, WorklogIdleLimit, forms.Biasa, false, false}, // away from the laptop
		{due, 0, forms.Tidur, false, false},
		{due, 0, forms.Fokus, false, false},                                               // do not disturb focus
		{due, 0, forms.Fokus, true, true},                                                 // worklog_bubble_in_focus: true
		{WorklogEvent{Type: "worklog.due", Age: time.Hour}, 0, forms.Biasa, false, false}, // stale after reconnect
		{WorklogEvent{Type: "worklog.closed"}, time.Hour, forms.Tidur, false, true},
	}
	for i, c := range cases {
		if got := ShowWorklogBubble(c.e, c.idle, c.form, c.inFocus); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestStatusLine(t *testing.T) {
	now := time.Now()
	a := &Agent{Cfg: testCfg()}
	a.conn, a.lastSync, a.paused = ConnOnline, now.Add(-12*time.Second), true
	if got := a.StatusLine(now); got != "Tunas · online · dijeda · sinkron 12 d lalu" {
		t.Fatalf("status %q", got)
	}
}
