package worklog

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/store"
)

type fakeTG struct {
	mu      sync.Mutex
	next    int
	sent    []bot.Reply
	edits   map[int]string
	deleted []int
}

func (f *fakeTG) SendReplyID(_ context.Context, _ int64, r bot.Reply) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.sent = append(f.sent, r)
	return 100 + f.next, nil
}

func (f *fakeTG) EditReply(_ context.Context, _ int64, id int, r bot.Reply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.edits == nil {
		f.edits = map[int]string{}
	}
	f.edits[id] = r.Text
	return nil
}

func (f *fakeTG) DeleteMessage(_ context.Context, _ int64, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

type rig struct {
	t   *testing.T
	s   *Service
	r   *Reminder
	tg  *fakeTG
	db  *store.DB
	now *time.Time
}

// newRig starts at 08:00 local on the given day.
func newRig(t *testing.T, day string) *rig {
	s, db, _ := newService(t)
	d := date(day)
	now := time.Date(d.Year(), d.Month(), d.Day(), 8, 0, 0, 0, jkt)
	s.Now = func() time.Time { return now }
	tg := &fakeTG{}
	r := &Reminder{Svc: s, TG: tg}
	s.OnSaved = r.OnSaved
	s.ExtraCallback = r.Callback
	return &rig{t: t, s: s, r: r, tg: tg, db: db, now: &now}
}

// at moves the clock to HH:MM the same day and ticks.
func (g *rig) at(clock string) {
	g.t.Helper()
	h, m, err := ParseClock(clock)
	if err != nil {
		g.t.Fatal(err)
	}
	n := *g.now
	*g.now = time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, jkt)
	if err := g.r.Tick(context.Background()); err != nil {
		g.t.Fatal(err)
	}
}

func (g *rig) sent() int { return len(g.tg.sent) }

func (g *rig) last() bot.Reply { return g.tg.sent[len(g.tg.sent)-1] }

func (g *rig) events(typ string) int {
	g.t.Helper()
	evs, err := g.db.PollEvents(context.Background(), 0, 0, 100, 0)
	if err != nil {
		g.t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func TestReminderSkipsDays(t *testing.T) {
	cases := map[string]func(g *rig){
		"weekend":        func(*rig) {},
		"holiday":        func(g *rig) { addHoliday(g, "2026-11-18", "holiday") },
		"libur override": func(g *rig) { _ = g.db.SetOverride(context.Background(), date("2026-11-18"), "off", "") },
		"worklog off":    func(g *rig) { _ = g.db.SetSetting(context.Background(), KeyEnabled, false) },
	}
	for name, setup := range cases {
		day := "2026-11-18"
		if name == "weekend" {
			day = "2026-11-21"
		}
		g := newRig(t, day)
		setup(g)
		g.at("10:30")
		g.at("13:30")
		if g.sent() != 0 {
			t.Errorf("%s: %d reminders, want none", name, g.sent())
		}
	}
	// cuti bersama and /kerja on a Saturday are reminded
	g := newRig(t, "2026-11-18")
	addHoliday(g, "2026-11-18", "leave")
	g.at("10:30")
	if g.sent() != 1 {
		t.Error("cuti bersama is still a working day")
	}
	g = newRig(t, "2026-11-21")
	_ = g.db.SetOverride(context.Background(), date("2026-11-21"), "work", "")
	g.at("10:30")
	if g.sent() != 1 {
		t.Error("/kerja on a Saturday is reminded")
	}
}

func addHoliday(g *rig, day, typ string) {
	g.t.Helper()
	if err := g.db.UpsertHolidays(context.Background(), []store.Holiday{{Day: date(day), Name: "contoh", Type: typ}}, *g.now); err != nil {
		g.t.Fatal(err)
	}
}

func TestReminderRecentEntryAndCatchUp(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("09:30")
	cmd(t, g.s, "log", "sudah mulai kerja")
	g.at("10:30") // entry 60 min ago < 90: expired, not sent
	if g.sent() != 0 {
		t.Fatal("recent entry: no reminder")
	}
	ps, _ := g.db.PromptsOn(context.Background(), date("2026-11-18"))
	if len(ps) != 1 || ps[0].Status != "expired" {
		t.Fatalf("prompts %+v", ps)
	}

	// restart in the middle of a slot: ≤ 30 min late is still sent, once
	g = newRig(t, "2026-11-18")
	g.at("10:59")
	if g.sent() != 1 {
		t.Fatal("29 min late is caught up")
	}
	g = newRig(t, "2026-11-18")
	g.at("11:01") // 31 min late
	g.at("11:02")
	if g.sent() != 0 {
		t.Fatal("more than 30 min late: expired without sending")
	}
	g.at("13:30")
	if g.sent() != 1 {
		t.Fatal("the next slot is sent normally")
	}
}

func TestReminderMaxPerDayAndSettings(t *testing.T) {
	g := newRig(t, "2026-11-18")
	ctx := context.Background()
	if r := g.r.Command(ctx, "jam 9:00,10:00,11:00,12:00,13:00"); !strings.Contains(r.Text, "09:00, 10:00, 11:00, 12:00, 13:00") {
		t.Fatalf("/worklog jam → %q", r.Text)
	}
	_ = g.db.SetSetting(ctx, KeyMaxPerDay, 2)
	for _, c := range []string{"09:00", "10:00", "11:00", "12:00", "13:00"} {
		g.at(c)
	}
	if g.sent() != 2 {
		t.Fatalf("max per day: %d sent", g.sent())
	}
	if r := g.r.Command(ctx, "jam 25:00"); !strings.Contains(r.Text, "tidak valid") {
		t.Fatal("bad time accepted")
	}
	if r := g.r.Command(ctx, "off"); !strings.Contains(r.Text, "mati") {
		t.Fatal("/worklog off")
	}
	if r := g.r.Command(ctx, "penutup 18:00"); !strings.Contains(r.Text, "penutup 18:00") {
		t.Fatal("/worklog penutup")
	}
}
