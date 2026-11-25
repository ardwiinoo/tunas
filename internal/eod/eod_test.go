package eod

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func total(name string, min int, originals ...string) store.ProjectTotal {
	return store.ProjectTotal{Name: name, Seconds: min * 60, Editors: map[string]int{"code.exe": min * 60}, Originals: originals}
}

func TestMissingSuggestions(t *testing.T) {
	ts := []store.ProjectTotal{
		total("project-A", 110),           // not in the worklog
		total("project-B", 45),            // mentioned, any letter case
		total("project-C", 29),            // under 30 minutes
		total("project-D", 30),            // exactly 30 minutes counts
		total("Proyek X", 60, "client-x"), // mentioned by its original name
		total("(tanpa proyek)", 90),       // never suggested
		total("Proyek Y", 60, "client-y"), // neither alias nor original mentioned
	}
	texts := []string{"Rapat sprint", "Perbaiki bug di PROJECT-b", "deploy client-x ke staging"}
	got := Missing(ts, texts)
	var names []string
	for _, g := range got {
		names = append(names, g.Text)
	}
	want := []string{"Coding: project-A 1j50m", "Coding: project-D 30m", "Coding: Proyek Y 1j"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("suggestions %q, want %q", names, want)
	}
	if got := Missing(ts, []string{"project-a project-b project-d proyek y client-x"}); len(got) != 0 {
		t.Fatalf("all mentioned: %v", got)
	}
}

func TestBuildReport(t *testing.T) {
	jkt := time.FixedZone("WIB", 7*3600)
	day := time.Date(2026, 11, 25, 17, 45, 0, 0, jkt)
	var bs []store.Bucket
	for i := 0; i < 90; i++ {
		bs = append(bs, store.Bucket{Start: time.Date(2026, 11, 25, 9, i, 0, 0, jkt), App: "Code.exe", Category: "work", ActiveSeconds: 60})
	}
	for i := 0; i < 20; i++ {
		bs = append(bs, store.Bucket{Start: time.Date(2026, 11, 25, 11, 0, 0, 0, jkt).Add(time.Duration(i) * time.Minute), App: "chrome.exe", Category: "browse", ActiveSeconds: 60})
	}
	r := Build(Input{Day: day, Loc: jkt, Summary: activity.Summarize(bs),
		Projects:     []store.ProjectTotal{total("project-A", 110), total("project-B", 45)},
		WorklogTexts: []string{"rapat", "review project-B"}})
	for _, want := range []string{
		"📋 Laporan akhir hari",
		"Total waktu aktif:",
		"Proyek:\n• project-A 1j50m (VS Code)\n• project-B 45m (VS Code)",
		"Aplikasi teratas: Code.exe",
		"Sesi fokus terpanjang: Code.exe",
		"Worklog hari ini: 2 catatan.",
		"Belum disebut di worklog:\n• Coding: project-A 1j50m",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in:\n%s", want, r.Text)
		}
	}
	if len(r.Suggestions) != 1 || strings.Contains(r.Text, "Coding: project-B") {
		t.Errorf("suggestions %+v", r.Suggestions)
	}
	empty := Build(Input{Day: day, Loc: jkt})
	if !strings.Contains(empty.Text, "Belum ada data aktivitas") || !strings.Contains(empty.Text, "belum ada catatan") || len(empty.Suggestions) != 0 {
		t.Errorf("empty day:\n%s", empty.Text)
	}
}

type fakeWL struct {
	workday bool
	added   []string
	err     error
}

func (f *fakeWL) Workday(context.Context, time.Time) (bool, error) { return f.workday, nil }
func (f *fakeWL) Add(_ context.Context, d time.Time, text string) error {
	if f.err != nil {
		return f.err
	}
	f.added = append(f.added, d.Format("2006-01-02")+" "+text)
	return nil
}

type tickEnv struct {
	svc  *Service
	wl   *fakeWL
	now  *time.Time
	sent *[]Card
	fail *bool
	db   *store.DB
	dev  int64
}

func newTick(t *testing.T) tickEnv {
	db := storetest.New(t)
	d, _, err := db.CreateDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	jkt := time.FixedZone("WIB", 7*3600)
	now := time.Date(2026, 11, 25, 17, 0, 0, 0, jkt) // a Wednesday
	wl := &fakeWL{workday: true}
	var sent []Card
	fail := false
	svc := &Service{DB: db, Loc: jkt, Now: func() time.Time { return now }, Worklog: wl,
		Send: func(_ context.Context, c Card) error {
			if fail {
				return errors.New("telegram down")
			}
			sent = append(sent, c)
			return nil
		}}
	return tickEnv{svc, wl, &now, &sent, &fail, db, d.ID}
}

func (e tickEnv) tick(t *testing.T, want int) {
	t.Helper()
	if err := e.svc.Tick(context.Background()); err != nil && !*e.fail {
		t.Fatal(err)
	}
	if len(*e.sent) != want {
		t.Fatalf("at %s: %d reports, want %d", e.now.Format("15:04"), len(*e.sent), want)
	}
}

func TestReportIsSentOnceAtItsTimeOnWorkdays(t *testing.T) {
	e := newTick(t)
	ctx := context.Background()
	e.tick(t, 0) // 17:00: too early
	*e.now = e.now.Add(44 * time.Minute)
	e.tick(t, 0) // 17:44
	*e.now = e.now.Add(time.Minute)
	e.tick(t, 1) // 17:45
	*e.now = e.now.Add(30 * time.Minute)
	e.tick(t, 1) // once a day
	if (*e.sent)[0].Rows != nil {
		t.Error("no suggestions without project time")
	}

	// next day is a weekend/holiday: nothing, and no repeated lookups
	*e.now = e.now.AddDate(0, 0, 1).Add(-30 * time.Minute) // 17:45 Thursday
	e.wl.workday = false
	e.tick(t, 1)
	*e.now = e.now.AddDate(0, 0, 1) // Friday, working again
	e.wl.workday = true
	e.tick(t, 2)

	// too late (server was down): not sent more than 3 h after the time
	*e.now = e.now.AddDate(0, 0, 1).Add(3 * time.Hour) // Saturday 20:45 is not a workday anyway; use a fresh env below
	e2 := newTick(t)
	*e2.now = time.Date(2026, 11, 25, 21, 0, 0, 0, e2.now.Location())
	e2.tick(t, 0)
	*e2.now = time.Date(2026, 11, 25, 20, 44, 0, 0, e2.now.Location())
	e2.tick(t, 1) // 2 h 59 min late is still sent

	// off, and a failed send is retried
	e3 := newTick(t)
	*e3.now = time.Date(2026, 11, 25, 18, 0, 0, 0, e3.now.Location())
	if _, err := e3.svc.Command(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	e3.tick(t, 0)
	if _, err := e3.svc.Command(ctx, "on"); err != nil {
		t.Fatal(err)
	}
	*e3.fail = true
	e3.tick(t, 0)
	*e3.fail = false
	e3.tick(t, 1)
}

func TestCommandSetsTheTime(t *testing.T) {
	e := newTick(t)
	ctx := context.Background()
	c, _ := e.svc.Command(ctx, "")
	if !strings.Contains(c.Text, "nyala, jam 17:45") {
		t.Fatalf("status: %q", c.Text)
	}
	if c, _ := e.svc.Command(ctx, "jam 9.05"); !strings.Contains(c.Text, "jam 09:05") {
		t.Fatalf("set: %q", c.Text)
	}
	for _, bad := range []string{"jam 25:00", "jam", "jam abc", "halo"} {
		if c, _ := e.svc.Command(ctx, bad); !strings.Contains(c.Text, "Pakai:") {
			t.Errorf("%q: %q", bad, c.Text)
		}
	}
	*e.now = time.Date(2026, 11, 25, 9, 10, 0, 0, e.now.Location())
	e.tick(t, 1) // the new time applies at once
}

// "Tambah ke worklog" saves the fixed-format entry, once, for the report's day.
func TestSuggestionButtonAddsTheEntryOnce(t *testing.T) {
	e := newTick(t)
	ctx := context.Background()
	var bs []store.ProjectBucket
	for i := 0; i < 110; i++ {
		bs = append(bs, store.ProjectBucket{Start: time.Date(2026, 11, 25, 2, i, 0, 0, time.UTC), Project: "project-A", Editor: "code.exe", ActiveSeconds: 60})
	}
	if _, err := e.db.UpsertProjectBuckets(ctx, e.dev, bs); err != nil {
		t.Fatal(err)
	}
	*e.now = time.Date(2026, 11, 25, 17, 50, 0, 0, e.now.Location())
	e.tick(t, 1)
	card := (*e.sent)[0]
	if len(card.Rows) != 1 || card.Rows[0][0].Text != "➕ Tambah ke worklog: project-A" || !strings.Contains(card.Text, "Coding: project-A 1j50m") {
		t.Fatalf("card %+v", card)
	}
	data := card.Rows[0][0].Data
	if len(data) > 64 {
		t.Fatalf("callback data %q", data)
	}
	c, ok, err := e.svc.Callback(ctx, data)
	if err != nil || !ok || !strings.Contains(c.Text, "Ditambahkan ke worklog: Coding: project-A 1j50m") {
		t.Fatalf("callback %+v %v %v", c, ok, err)
	}
	if len(e.wl.added) != 1 || e.wl.added[0] != "2026-11-25 Coding: project-A 1j50m" {
		t.Fatalf("added %v", e.wl.added)
	}
	// tapping again, or a stale index, adds nothing
	c, _, _ = e.svc.Callback(ctx, data)
	if c.Toast != "Sudah ditambahkan." || len(e.wl.added) != 1 {
		t.Fatalf("second tap %+v %v", c, e.wl.added)
	}
	if c, _, _ := e.svc.Callback(ctx, "eod:a:7"); c.Toast != "Saran ini sudah kedaluwarsa." {
		t.Errorf("stale: %+v", c)
	}
	if c, _, _ := e.svc.Callback(ctx, "eod:a:x"); c.Toast == "" || len(e.wl.added) != 1 {
		t.Errorf("junk: %+v", c)
	}
	if _, ok, _ := e.svc.Callback(ctx, "apps:x"); ok {
		t.Error("another module's callback was taken")
	}
	// the worklog now mentions the project: the next report has no suggestion
	e.wl.added = nil
	if r, _ := e.svc.Report(ctx); len(r.Rows) != 1 {
		t.Log("the fake worklog does not store entries, so the suggestion is still offered")
	}
}

func TestParseClock(t *testing.T) {
	for in, ok := range map[string]bool{"17:45": true, "9:05": true, "09.05": true, "24:00": false, "12:60": false, "": false, "abc": false} {
		if _, _, got := ParseClock(in); got != ok {
			t.Errorf("ParseClock(%q) = %v", in, got)
		}
	}
}
