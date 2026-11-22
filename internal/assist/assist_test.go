package assist

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
	"github.com/ardwiinoo/tunas/modes"
)

var jkt, _ = time.LoadLocation("Asia/Jakarta")

// fakeLLM returns scripted replies in order and records every payload.
type fakeLLM struct {
	mu      sync.Mutex
	replies []string
	err     error
	seen    [][]llm.Message
}

func (f *fakeLLM) Complete(_ context.Context, msgs []llm.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, msgs)
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) == 0 {
		return "", errors.New("no scripted reply")
	}
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

func (f *fakeLLM) allContent() string {
	var b strings.Builder
	for _, ms := range f.seen {
		for _, m := range ms {
			b.WriteString(m.Content + "\n")
		}
	}
	return b.String()
}

type notifier struct{ msgs []string }

func (n *notifier) NotifyOwner(_ context.Context, t string) error {
	n.msgs = append(n.msgs, t)
	return nil
}

func setup(t *testing.T, f llm.Completer) (*Assistant, *store.DB, *time.Time) {
	db := storetest.New(t)
	ms, err := modes.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, jkt) // Monday
	a := New(db, f, ms, jkt, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	return a, db, &now
}

func TestParseIntent(t *testing.T) {
	good := []string{
		`{"intent":"reminder","when":"17:00","text":"kirim laporan"}`,
		"```json\n{\"intent\":\"task\",\"title\":\"beli kopi\"}\n```",
		`{"intent":"question","template":"top_apps","range":"this_week"}`,
		`{"intent":"chat"}`,
	}
	for _, g := range good {
		if _, err := ParseIntent(g); err != nil {
			t.Errorf("%s: %v", g, err)
		}
	}
	bad := []string{
		`halo`,
		`{"intent":"reminder","text":"x"}`,
		`{"intent":"reminder","when":"17:00","text":"x","title":"y"}`,
		`{"intent":"reminder","when":"17:00","text":"x","confidence":0.9}`,
		`{"intent":"task"}`,
		`{"intent":"question","template":"select * from tasks","range":"today"}`,
		`{"intent":"question","template":"top_apps","range":"forever"}`,
		`{"intent":"chat","text":"x"}`,
		`{"intent":"delete_all"}`,
		`{"intent":"task","title":"` + strings.Repeat("a", 300) + `"}`,
	}
	for _, b := range bad {
		if _, err := ParseIntent(b); err == nil {
			t.Errorf("%s: expected rejection", b)
		}
	}
}

func TestReminderConfirmFlow(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"intent":"reminder","when":"17:00","text":"kirim laporan"}`}}
	a, db, _ := setup(t, f)
	ctx := context.Background()

	r := a.HandleText(ctx, "ingatkan aku jam 5 sore kirim laporan")
	if r.Text != "Buat pengingat Sen 23 Nov 17:00 WIB: kirim laporan?" || len(r.Buttons) != 2 {
		t.Fatalf("reply = %+v", r)
	}
	if pend, _ := db.PendingReminders(ctx, 10); len(pend) != 0 {
		t.Fatal("must not save before confirmation")
	}
	got := a.HandleCallback(ctx, r.Buttons[0].Data)
	if !strings.Contains(got, "Oke, diingatkan Sen 23 Nov 17:00 WIB: kirim laporan") {
		t.Fatalf("confirm = %q", got)
	}
	pend, _ := db.PendingReminders(ctx, 10)
	if len(pend) != 1 || !pend[0].RemindAt.Equal(time.Date(2026, 11, 23, 17, 0, 0, 0, jkt)) {
		t.Fatalf("pending = %+v", pend)
	}
	if got := a.HandleCallback(ctx, r.Buttons[0].Data); !strings.Contains(got, "kedaluwarsa") {
		t.Fatalf("double confirm = %q", got)
	}
	if !strings.Contains(f.seen[0][0].Content, "2026-11-23 10:00 WIB") {
		t.Fatal("classifier needs current time context")
	}
}

func TestTaskCancelAndExpiry(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"intent":"task","title":"beli kopi"}`, `{"intent":"task","title":"x"}`}}
	a, db, now := setup(t, f)
	ctx := context.Background()
	r := a.HandleText(ctx, "catat tugas beli kopi")
	if got := a.HandleCallback(ctx, r.Buttons[1].Data); got != "Oke, dibatalkan." {
		t.Fatalf("cancel = %q", got)
	}
	r = a.HandleText(ctx, "catat x")
	*now = now.Add(11 * time.Minute)
	if got := a.HandleCallback(ctx, r.Buttons[0].Data); !strings.Contains(got, "kedaluwarsa") {
		t.Fatalf("expired = %q", got)
	}
	if ts, _ := db.OpenTasks(ctx, 10); len(ts) != 0 {
		t.Fatalf("tasks = %+v", ts)
	}
}

func TestModelTimeIsValidatedByCode(t *testing.T) {
	f := &fakeLLM{replies: []string{
		`{"intent":"reminder","when":"2026-11-01 09:00","text":"lewat"}`,
		`{"intent":"reminder","when":"jam lima sore","text":"x"}`,
	}}
	a, _, _ := setup(t, f)
	for i := 0; i < 2; i++ {
		r := a.HandleText(context.Background(), "ingatkan")
		if len(r.Buttons) != 0 || !strings.Contains(r.Text, "tidak valid") {
			t.Fatalf("reply %d = %+v", i, r)
		}
	}
}

func TestLLMUnavailableFallsBack(t *testing.T) {
	a, _, _ := setup(t, &fakeLLM{err: &llm.StatusError{Code: 429}})
	if r := a.HandleText(context.Background(), "halo"); r.Text != fallbackNoLLM {
		t.Fatalf("reply = %q", r.Text)
	}
	a2, _, _ := setup(t, llm.Disabled{})
	if got := a2.Ask(context.Background(), "apa?"); got != fallbackNoLLM {
		t.Fatalf("ask = %q", got)
	}
}

func TestDailyCapWithDB(t *testing.T) {
	inner := &fakeLLM{replies: []string{`{"intent":"chat"}`, "Halo juga!"}}
	db := storetest.New(t)
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, jkt)
	capped := &llm.Capped{Inner: inner, Usage: db, Cap: 2, Now: func() time.Time { return now }, Loc: jkt}
	ms, _ := modes.Load()
	a := New(db, capped, ms, jkt, func() time.Time { return now }, slog.New(slog.DiscardHandler))
	if r := a.HandleText(context.Background(), "halo"); r.Text != "Halo juga!" {
		t.Fatalf("first = %q", r.Text)
	}
	if r := a.HandleText(context.Background(), "halo lagi"); r.Text != fallbackNoLLM {
		t.Fatalf("over cap = %q", r.Text)
	}
	if len(inner.seen) != 2 {
		t.Fatalf("inner called %d times, want 2", len(inner.seen))
	}
}

func seedActivity(t *testing.T, db *store.DB, day time.Time) {
	ctx := context.Background()
	d, _, err := db.CreateDevice(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	var bs []store.Bucket
	for i := 0; i < 90; i++ {
		bs = append(bs, store.Bucket{Start: day.Add(time.Duration(i) * time.Minute), App: "Code.exe", Category: "work", ActiveSeconds: 60})
	}
	for i := 0; i < 20; i++ {
		bs = append(bs, store.Bucket{Start: day.Add(time.Duration(200+i) * time.Minute), App: "Discord.exe", Category: "distract", ActiveSeconds: 60})
	}
	if err := db.UpsertBuckets(ctx, d.ID, bs); err != nil {
		t.Fatal(err)
	}
}

var reTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)

func TestAskUsesTemplateAndOnlyAggregates(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"template":"top_apps","range":"this_week"}`, "Minggu ini kamu paling lama di Code.exe (1 j 30 m)."}}
	a, db, _ := setup(t, f)
	seedActivity(t, db, time.Date(2026, 11, 23, 8, 0, 0, 0, jkt))

	got := a.Ask(context.Background(), "minggu ini paling lama di aplikasi apa?")
	if !strings.Contains(got, "Code.exe") {
		t.Fatalf("answer = %q", got)
	}
	facts := f.seen[1][1].Content
	if !strings.Contains(facts, "1. Code.exe — 1 j 30 m") || !strings.Contains(facts, "minggu ini (Sen 23 Nov s.d. Sen 23 Nov)") {
		t.Fatalf("facts = %q", facts)
	}
	all := f.allContent()
	if reTimestamp.MatchString(all) || strings.Contains(strings.ToLower(all), "bucket") || strings.Contains(all, "active_seconds") {
		t.Fatalf("raw data leaked to LLM: %q", all)
	}
}

func TestAskRejectsBadTemplate(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"template":"raw_sql","range":"today","sql":"drop table tasks"}`}}
	a, _, _ := setup(t, f)
	if got := a.Ask(context.Background(), "x"); !strings.Contains(got, "belum bisa menjawab") || len(f.seen) != 1 {
		t.Fatalf("got %q after %d calls", got, len(f.seen))
	}
}

func TestFactsTemplates(t *testing.T) {
	a, db, _ := setup(t, &fakeLLM{})
	seedActivity(t, db, time.Date(2026, 11, 23, 8, 0, 0, 0, jkt))
	ctx := context.Background()
	for tpl, want := range map[string]string{
		"longest_session": "Sesi terpanjang: Code.exe selama 1 j 30 m, Sen 23 Nov 08:00–09:30",
		"total_active":    "Total waktu aktif: 1 j 50 m",
		"by_category":     "• hiburan: 20 m",
	} {
		got, err := a.Facts(ctx, tpl, "today")
		if err != nil || !strings.Contains(got, want) {
			t.Errorf("%s: %q %v", tpl, got, err)
		}
	}
	if got, _ := a.Facts(ctx, "top_apps", "yesterday"); !strings.Contains(got, "Tidak ada data") {
		t.Errorf("yesterday = %q", got)
	}
}

func TestRange(t *testing.T) {
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, jkt) // Wednesday
	from, to := Range("this_week", now, jkt)
	if !from.Equal(time.Date(2026, 11, 23, 0, 0, 0, 0, jkt)) || !to.Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, jkt)) {
		t.Fatalf("this_week = %v %v", from, to)
	}
	from, _ = Range("this_week", time.Date(2026, 11, 29, 1, 0, 0, 0, jkt), jkt) // Sunday
	if !from.Equal(time.Date(2026, 11, 23, 0, 0, 0, 0, jkt)) {
		t.Fatalf("sunday week start = %v", from)
	}
	from, _ = Range("this_month", now, jkt)
	if from.Day() != 1 {
		t.Fatal("month start")
	}
}

func TestDailySummary(t *testing.T) {
	f := &fakeLLM{replies: []string{"Hari ini kamu fokus 1 j 30 m di Code.exe. Mantap!"}}
	a, db, now := setup(t, f)
	ctx := context.Background()
	n := &notifier{}

	*now = time.Date(2026, 11, 23, 20, 59, 0, 0, jkt)
	if sent, _ := a.DailyTick(ctx, n); sent {
		t.Fatal("sent before 21:00")
	}
	*now = time.Date(2026, 11, 23, 21, 0, 0, 0, jkt)
	if sent, err := a.DailyTick(ctx, n); sent || err != nil || len(n.msgs) != 0 {
		t.Fatalf("no data should skip: %v %v", sent, err)
	}

	// next day with data
	seedActivity(t, db, time.Date(2026, 11, 24, 8, 0, 0, 0, jkt))
	*now = time.Date(2026, 11, 24, 21, 5, 0, 0, jkt)
	if sent, err := a.DailyTick(ctx, n); !sent || err != nil {
		t.Fatalf("not sent: %v", err)
	}
	if sent, _ := a.DailyTick(ctx, n); sent || len(n.msgs) != 1 {
		t.Fatal("sent twice")
	}
	if !strings.HasPrefix(n.msgs[0], "🌙 Ringkasan Sel 24 Nov\nHari ini kamu fokus") {
		t.Fatalf("msg = %q", n.msgs[0])
	}
	if !strings.Contains(f.seen[0][0].Content, "Tunas") {
		t.Fatal("persona voice missing")
	}

	// custom time via settings; LLM down → template fallback
	_ = db.SetSetting(ctx, SummaryTimeKey, "07:30")
	f.err = errors.New("down")
	seedMore := time.Date(2026, 11, 25, 6, 0, 0, 0, jkt)
	d, _ := db.ListDevices(ctx)
	_ = db.UpsertBuckets(ctx, d[0].ID, []store.Bucket{{Start: seedMore, App: "Code.exe", Category: "work", ActiveSeconds: 60}})
	*now = time.Date(2026, 11, 25, 7, 31, 0, 0, jkt)
	if sent, _ := a.DailyTick(ctx, n); !sent || !strings.Contains(n.msgs[1], "Total aktif: 1 m") {
		t.Fatalf("fallback = %q", n.msgs)
	}
}

func TestSetForm(t *testing.T) {
	a, db, _ := setup(t, &fakeLLM{})
	ctx := context.Background()
	if got, _ := a.SetForm(ctx, "fokus"); got != "Form diatur ke Fokus." || a.ActiveForm(ctx) != "fokus" {
		t.Fatalf("set = %q", got)
	}
	if got, _ := a.SetForm(ctx, "naga"); !strings.Contains(got, "tidak dikenal") {
		t.Fatalf("bad = %q", got)
	}
	if got, _ := a.SetForm(ctx, "auto"); got != "Form kembali otomatis." || a.ActiveForm(ctx) != "biasa" {
		t.Fatalf("auto = %q", got)
	}
	evs, _ := db.EventsAfter(ctx, 1, 0, 10)
	if len(evs) != 2 || evs[0].Type != "form.set" || !strings.Contains(string(evs[1].Payload), "auto") {
		t.Fatalf("events = %+v", evs)
	}
}
