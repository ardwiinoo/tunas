package worklog

import (
	"context"
	"fmt"
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

// 2026-11-18 is a Wednesday, 21/22 a weekend.
func TestReminderWorkdayThreeSlots(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:29")
	if g.sent() != 0 {
		t.Fatal("too early")
	}
	g.at("10:30")
	if g.sent() != 1 || !strings.Contains(g.last().Text, "📝 Worklog · Rabu 18 Nov · 10:30") || !strings.Contains(g.last().Text, "belum ada catatan") {
		t.Fatalf("first prompt %+v", g.tg.sent)
	}
	g.at("10:31")
	g.at("10:45")
	if g.sent() != 1 {
		t.Fatal("a slot is sent exactly once")
	}
	if g.events("worklog.due") != 1 {
		t.Fatal("worklog.due for the agent")
	}
	// Nanti: the message is edited, no extra reminder before the next slot
	later := button(t, g.last(), "⏭")
	r := cb(t, g.s, later)
	if r.Text != "⏭ Ditunda ke pengingat berikutnya." {
		t.Fatalf("later → %q", r.Text)
	}
	for _, c := range []string{"11:00", "12:00", "13:29"} {
		g.at(c)
	}
	if g.sent() != 1 {
		t.Fatal("Nanti must not add reminders between slots")
	}
	g.at("13:30")
	g.at("16:30")
	if g.sent() != 3 || len(g.tg.deleted) != 2 {
		t.Fatalf("three reminders on a working day, old ones removed: sent %d deleted %v", g.sent(), g.tg.deleted)
	}
	g.at("17:30")
	if g.sent() != 3 {
		t.Fatal("no closing reminder unless the last slot was postponed")
	}
}

func TestReminderClosingAfterLastPostponed(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:30")
	cb(t, g.s, button(t, g.last(), "🙈")) // skip → nothing more today
	g.at("13:30")
	g.at("16:30")
	if g.sent() != 1 {
		t.Fatal("Lewati hari ini stops the day")
	}

	g = newRig(t, "2026-11-19")
	g.at("16:30")
	last := button(t, g.last(), "⏭")
	if r := cb(t, g.s, last); !strings.Contains(r.Text, "penutup jam 17:30") {
		t.Fatalf("postponing the last slot announces the closing reminder: %q", r.Text)
	}
	g.at("17:29")
	g.at("17:30")
	if g.sent() != 2 {
		t.Fatal("closing reminder")
	}
	closing := g.last()
	if len(closing.Rows) != 1 || len(closing.Rows[0]) != 2 || strings.Contains(fmt.Sprint(closing.Rows), "Nanti") {
		t.Fatalf("closing has only Tulis/Lewati: %+v", closing.Rows)
	}
	g.at("17:45")
	if g.sent() != 2 {
		t.Fatal("closing is sent once, then it stops")
	}
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

func TestReminderAnsweredByEntry(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:30")
	// Tulis sekarang → the next text is today's entry and closes the prompt
	ask := cb(t, g.s, button(t, g.last(), "✍️"))
	if !strings.Contains(ask.Text, "Kirim catatanmu") {
		t.Fatalf("write prompt %q", ask.Text)
	}
	r, ok := g.s.PendingText(context.Background(), "mengerjakan fitur worklog")
	if !ok || r.Text != "✅ Tersimpan (1 catatan hari ini)" {
		t.Fatalf("saved %+v", r)
	}
	if g.tg.edits[101] != "✅ Worklog sudah diisi." || g.events("worklog.closed") != 1 {
		t.Fatalf("prompt closed: edits %v", g.tg.edits)
	}
	if r := cb(t, g.s, "wln:1"); !strings.Contains(r.Text, "sudah selesai") {
		t.Fatalf("closed prompt buttons: %q", r.Text)
	}

	// the agent answers "later": Telegram's message is edited, event for both
	g = newRig(t, "2026-11-19")
	g.at("10:30")
	ps, _ := g.db.PromptsOn(context.Background(), date("2026-11-19"))
	if _, err := g.r.Answer(context.Background(), ps[0].ID, "later", "agent"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.tg.edits[101], "Ditunda") {
		t.Fatalf("telegram message after agent answer: %v", g.tg.edits)
	}
	if _, err := g.r.Answer(context.Background(), ps[0].ID, "skip", "agent"); err != nil {
		t.Fatal("later → skip is allowed")
	}
	if _, err := g.r.Answer(context.Background(), ps[0].ID, "skip", "agent"); err != ErrPromptClosed {
		t.Fatalf("answering a closed prompt: %v", err)
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
