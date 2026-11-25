package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeCalAPI struct {
	down    bool
	status  int // when set, answer with this HTTP status error
	month   map[string]CalMonth
	days    map[string]CalDayView
	edited  []int64
	deleted []int64
}

func (f *fakeCalAPI) err() error {
	switch {
	case f.down:
		return errors.New("dial tcp: connection refused")
	case f.status != 0:
		return &StatusError{Code: f.status}
	}
	return nil
}

func (f *fakeCalAPI) CalMonth(_ context.Context, m string) (CalMonth, error) {
	if err := f.err(); err != nil {
		return CalMonth{}, err
	}
	return f.month[m], nil
}

func (f *fakeCalAPI) CalDay(_ context.Context, d string) (CalDayView, error) {
	if err := f.err(); err != nil {
		return CalDayView{}, err
	}
	return f.days[d], nil
}

func (f *fakeCalAPI) EditEntry(_ context.Context, id int64, _ string) error {
	if err := f.err(); err != nil {
		return err
	}
	f.edited = append(f.edited, id)
	return nil
}

func (f *fakeCalAPI) DeleteEntry(_ context.Context, id int64) error {
	if err := f.err(); err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func newCal(t *testing.T) (*Calendar, *fakeCalAPI, string) {
	path := filepath.Join(t.TempDir(), "calendar.json")
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	api := &fakeCalAPI{
		month: map[string]CalMonth{"2026-11": {Month: "2026-11", Workdays: 12, Filled: 10, Days: []CalDay{{D: 1, Kind: "weekend"}}}},
		days:  map[string]CalDayView{"2026-11-17": {Date: "2026-11-17", Entries: []CalEntry{{ID: 1, Text: "rapat"}}}},
	}
	return &Calendar{API: api, Cache: &CalCache{Path: path, Now: func() time.Time { now = now.Add(time.Second); return now }}}, api, path
}

func TestCalendarServesTheCacheOffline(t *testing.T) {
	cal, api, path := newCal(t)
	ctx := context.Background()
	m, stale, err := cal.Month(ctx, "2026-11")
	if err != nil || stale || m.Filled != 10 {
		t.Fatalf("online %+v %v %v", m, stale, err)
	}
	d, stale, err := cal.Day(ctx, "2026-11-17")
	if err != nil || stale || len(d.Entries) != 1 {
		t.Fatalf("online day %+v %v %v", d, stale, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("cache file: %v", err)
	}

	api.down = true // the Wi-Fi is off
	m, stale, err = cal.Month(ctx, "2026-11")
	if err != nil || !stale || m.Filled != 10 {
		t.Fatalf("offline month %+v stale=%v %v: the last answer is shown, marked as old", m, stale, err)
	}
	if d, stale, err = cal.Day(ctx, "2026-11-17"); err != nil || !stale || d.Entries[0].Text != "rapat" {
		t.Fatalf("offline day %+v %v %v", d, stale, err)
	}
	// a month never looked at has nothing to show
	if _, _, err := cal.Month(ctx, "2026-05"); err == nil {
		t.Fatal("an unknown month offline must fail")
	}
	// a 5xx also means "not reachable"
	api.down, api.status = false, 503
	if _, stale, err := cal.Month(ctx, "2026-11"); err != nil || !stale {
		t.Fatalf("503: %v %v", stale, err)
	}
	// but a plain answer such as 400 is not hidden by the cache
	api.status = 400
	if _, _, err := cal.Month(ctx, "2026-11"); err == nil {
		t.Fatal("a 400 must be shown, not masked")
	}
	// a restart keeps the cache (it is a file)
	again := &Calendar{API: api, Cache: &CalCache{Path: path, Now: time.Now}}
	api.status, api.down = 0, true
	if _, stale, err := again.Month(ctx, "2026-11"); err != nil || !stale {
		t.Fatalf("after restart: %v %v", stale, err)
	}
}

func TestCalendarEditsNeedTheServer(t *testing.T) {
	cal, api, _ := newCal(t)
	ctx := context.Background()
	_, _, _ = cal.Day(ctx, "2026-11-17")
	if err := cal.Edit(ctx, 1, "baru"); err != nil || len(api.edited) != 1 {
		t.Fatalf("edit %v %v", err, api.edited)
	}
	// an edit makes the cached days and months out of date: they are dropped
	api.down = true
	if _, _, err := cal.Day(ctx, "2026-11-17"); err == nil {
		t.Fatal("the cache survived an edit")
	}
	if err := cal.Edit(ctx, 1, "x"); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline edit: %v", err)
	}
	if err := cal.Delete(ctx, 1); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline delete: %v", err)
	}
	api.down, api.status = false, 404
	var se *StatusError
	if err := cal.Delete(ctx, 9); !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("a gone entry is reported as it is: %v", err)
	}
}

func TestCalCacheKeepsOnlyTheNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	n := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &CalCache{Path: path, Now: func() time.Time { n = n.Add(time.Minute); return n }}
	for i := 0; i < maxCachedMonths+4; i++ {
		c.put(fmt.Sprintf("m:2026-%02d", i+1), CalMonth{Month: fmt.Sprint(i)})
	}
	for i := 0; i < maxCachedDays+5; i++ {
		c.put(fmt.Sprintf("d:2026-01-%02d-%d", i%28+1, i), CalDayView{Date: fmt.Sprint(i)})
	}
	m := c.load()
	months, days := 0, 0
	for k := range m {
		switch k[0] {
		case 'm':
			months++
		case 'd':
			days++
		}
	}
	if months != maxCachedMonths || days != maxCachedDays {
		t.Fatalf("kept %d months and %d days", months, days)
	}
	var got CalMonth
	if !c.get(fmt.Sprintf("m:2026-%02d", maxCachedMonths+4), &got) || c.get("m:2026-01", &got) {
		t.Fatal("the newest stay, the oldest go")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("cache file mode %v (%v)", info.Mode(), err)
	}
	// a broken file is treated as empty
	if err := os.WriteFile(path, []byte("{rusak"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.get("m:2026-10", &got) {
		t.Fatal("a broken cache must not return data")
	}
}

func TestClientCalendarCalls(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.RequestURI(), r.Header.Get("Authorization"), r.Method
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/agent/worklog/month"):
			_ = json.NewEncoder(w).Encode(CalMonth{Month: "2026-11", Workdays: 3})
		case strings.HasPrefix(r.URL.Path, "/v1/agent/worklog/day"):
			_ = json.NewEncoder(w).Encode(CalDayView{Date: "2026-11-17", Entries: []CalEntry{{ID: 4, Text: "x"}}})
		case r.URL.Path == "/v1/agent/command":
			_ = json.NewEncoder(w).Encode(CommandResult{Kind: "text", Text: "halo"})
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tnd_secret")
	ctx := context.Background()
	if m, err := c.CalMonth(ctx, "2026-11"); err != nil || m.Workdays != 3 || gotPath != "/v1/agent/worklog/month?m=2026-11" || gotAuth != "Bearer tnd_secret" {
		t.Fatalf("month %+v %v %s %s", m, err, gotPath, gotAuth)
	}
	if d, err := c.CalDay(ctx, "2026-11-17"); err != nil || d.Entries[0].ID != 4 || gotPath != "/v1/agent/worklog/day?d=2026-11-17" {
		t.Fatalf("day %+v %v %s", d, err, gotPath)
	}
	if err := c.EditEntry(ctx, 4, "baru"); err != nil || gotMethod != "PATCH" || gotPath != "/v1/agent/worklog/entries/4" || body["text"] != "baru" {
		t.Fatalf("edit %v %s %s %v", err, gotMethod, gotPath, body)
	}
	if err := c.DeleteEntry(ctx, 4); err != nil || gotMethod != "DELETE" || gotPath != "/v1/agent/worklog/entries/4" {
		t.Fatalf("delete %v %s %s", err, gotMethod, gotPath)
	}
	if r, err := c.RunCommand(ctx, "today", ""); err != nil || r.Kind != "text" || r.Text != "halo" || body["cmd"] != "today" {
		t.Fatalf("command %+v %v %v", r, err, body)
	}
}

func TestPlainTextForTheBubble(t *testing.T) {
	cases := map[string]string{
		"✅ Tersimpan (3 catatan hari ini)":                 "√ Tersimpan (3 catatan hari ini)",
		"⚠️ Disk 90%":                                      "! Disk 90%",
		"📊 Hari ini (Rab 25 Nov)\nTotal aktif: 6 j":        "Hari ini (Rab 25 Nov)\nTotal aktif: 6 j",
		"💻 Proyek hari ini — 3j\n• project-A 2j (VS Code)": "Proyek hari ini — 3j\n• project-A 2j (VS Code)",
		"Laptop\n   Memori 54 MB ⚠️ (target)":              "Laptop\n   Memori 54 MB ! (target)",
		"a\u0007b":       "ab",
		"👨‍👩‍👧 keluarga": "keluarga",
		"":               "",
	}
	for in, want := range cases {
		if got := PlainText(in); got != want {
			t.Errorf("PlainText(%q) = %q, want %q", in, got, want)
		}
	}
}
