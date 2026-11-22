package reminders

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type fakeNotifier struct {
	mu    sync.Mutex
	msgs  []string
	fail  bool
	delay time.Duration
}

func (f *fakeNotifier) NotifyOwner(_ context.Context, text string) error {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail && !strings.HasPrefix(text, "⚠️") {
		return errors.New("telegram down")
	}
	f.msgs = append(f.msgs, text)
	return nil
}

func (f *fakeNotifier) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*store.DB, *fakeNotifier, *clock, *Scheduler) {
	db := storetest.New(t)
	n := &fakeNotifier{}
	c := &clock{t: at(2026, 11, 23, 10, 0)}
	s := NewScheduler(db, n, c.Now, jkt, slog.New(slog.DiscardHandler))
	return db, n, c, s
}

func TestSchedulerSendsOnce(t *testing.T) {
	db, n, c, s := setup(t)
	ctx := context.Background()
	if _, err := db.CreateReminder(ctx, "tes", c.Now().Add(2*time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.Tick(ctx); k != 0 {
		t.Fatal("sent before due")
	}
	c.Add(2 * time.Minute)
	if k, err := s.Tick(ctx); k != 1 || err != nil {
		t.Fatalf("tick = %d %v", k, err)
	}
	c.Add(time.Minute)
	if k, _ := s.Tick(ctx); k != 0 {
		t.Fatal("sent twice")
	}
	msgs := n.sent()
	if len(msgs) != 1 || msgs[0] != "⏰ tes" {
		t.Fatalf("msgs = %q", msgs)
	}
	evs, _ := db.EventsAfter(ctx, 1, 0, 10)
	if len(evs) != 1 || evs[0].Type != "reminder.due" || !strings.Contains(string(evs[0].Payload), "tes") {
		t.Fatalf("events = %+v", evs)
	}
	pending, _ := db.PendingReminders(ctx, 10)
	if len(pending) != 0 {
		t.Fatalf("pending = %+v", pending)
	}
}

func TestSchedulerConcurrentInstancesSendOnce(t *testing.T) {
	db, n, c, s := setup(t)
	ctx := context.Background()
	n.delay = 50 * time.Millisecond
	for i := 0; i < 5; i++ {
		if _, err := db.CreateReminder(ctx, "r", c.Now(), ""); err != nil {
			t.Fatal(err)
		}
	}
	s2 := NewScheduler(db, n, c.Now, jkt, slog.New(slog.DiscardHandler))
	var wg sync.WaitGroup
	for _, sch := range []*Scheduler{s, s2, s} {
		wg.Go(func() { _, _ = sch.Tick(ctx) })
	}
	wg.Wait()
	if got := len(n.sent()); got != 5 {
		t.Fatalf("sent %d messages, want 5", got)
	}
}

func TestSchedulerLateAndRecurring(t *testing.T) {
	db, n, _, s := setup(t)
	ctx := context.Background()
	// due 07:00 daily, server was down until 10:00
	if _, err := db.CreateReminder(ctx, "minum air", at(2026, 11, 23, 7, 0), "daily"); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.Tick(ctx); k != 1 {
		t.Fatal("not sent")
	}
	msg := n.sent()[0]
	if !strings.Contains(msg, "terlambat 3 j") || !strings.Contains(msg, "tiap hari") {
		t.Fatalf("msg = %q", msg)
	}
	pending, _ := db.PendingReminders(ctx, 10)
	if len(pending) != 1 || !pending[0].RemindAt.Equal(at(2026, 11, 24, 7, 0)) || pending[0].Recurrence != "daily" {
		t.Fatalf("next = %+v", pending)
	}
}

func TestSchedulerRetryThenFail(t *testing.T) {
	db, n, c, s := setup(t)
	ctx := context.Background()
	n.fail = true
	r, _ := db.CreateReminder(ctx, "x", c.Now(), "")

	for attempt := 1; attempt <= 5; attempt++ {
		_, _ = s.Tick(ctx)
		// a second tick within the retry window must not retry
		c.Add(30 * time.Second)
		_, _ = s.Tick(ctx)
		c.Add(30 * time.Second)
	}
	var status string
	var attempts int
	_ = db.Pool.QueryRow(ctx, `select status, attempts from reminders where id=$1`, r.ID).Scan(&status, &attempts)
	if status != "failed" || attempts != 5 {
		t.Fatalf("status=%s attempts=%d", status, attempts)
	}
	msgs := n.sent()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "gagal dikirim 5 kali") {
		t.Fatalf("failure notice = %q", msgs)
	}
}

func TestSchedulerRecoversAfterFailure(t *testing.T) {
	db, n, c, s := setup(t)
	ctx := context.Background()
	n.fail = true
	_, _ = db.CreateReminder(ctx, "y", c.Now(), "")
	_, _ = s.Tick(ctx)
	n.mu.Lock()
	n.fail = false
	n.mu.Unlock()
	c.Add(time.Minute)
	if k, _ := s.Tick(ctx); k != 1 || len(n.sent()) != 1 {
		t.Fatalf("retry did not deliver: %q", n.sent())
	}
}
