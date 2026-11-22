package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/api"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func openBuf(t *testing.T) *Buffer {
	b, err := OpenBuffer(filepath.Join(t.TempDir(), "buf.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestBufferAddClampAndSync(t *testing.T) {
	ctx := context.Background()
	b := openBuf(t)
	m := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ { // 80s of samples in one minute → clamped to 60
		if err := b.Add(ctx, m.Add(5*time.Second), "Code.exe", "work", 10, 0); err != nil {
			t.Fatal(err)
		}
	}
	_ = b.Add(ctx, m, "Code.exe", "work", 0, 10) // idle cannot exceed what is left
	pend, _ := b.Pending(ctx, 10)
	if len(pend) != 1 || pend[0].ActiveSeconds != 60 || pend[0].IdleSeconds != 0 || !pend[0].Start.Equal(m) {
		t.Fatalf("pending = %+v", pend)
	}

	// a bucket updated after being read stays dirty
	_ = b.Add(ctx, m.Add(time.Minute), "a.exe", "other", 10, 0)
	pend, _ = b.Pending(ctx, 10)
	_ = b.Add(ctx, m.Add(time.Minute), "a.exe", "other", 10, 0)
	_ = b.MarkSynced(ctx, pend)
	pend2, _ := b.Pending(ctx, 10)
	if len(pend2) != 1 || pend2[0].App != "a.exe" || pend2[0].ActiveSeconds != 20 {
		t.Fatalf("pending after concurrent update = %+v", pend2)
	}
	if n, _ := b.Prune(ctx, m.Add(time.Minute)); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if total, dirty, _ := b.Count(ctx); total != 1 || dirty != 1 {
		t.Fatalf("count = %d %d", total, dirty)
	}

	_ = b.SetKV(ctx, "k", "1")
	_ = b.SetKV(ctx, "k", "2")
	if v, _ := b.GetKV(ctx, "k"); v != "2" {
		t.Fatalf("kv = %q", v)
	}
}

// flakyAPI forwards to a real client but can simulate a network outage.
type flakyAPI struct {
	*Client
	down atomic.Bool
}

func (f *flakyAPI) PostActivity(ctx context.Context, bs []Bucket) error {
	if f.down.Load() {
		return errors.New("network unreachable")
	}
	return f.Client.PostActivity(ctx, bs)
}

func testCfg() *config.Agent {
	c := config.DefaultAgent()
	c.DeviceToken = "x"
	return c
}

func TestOfflineThenOnlineNoLossNoDuplicates(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	_, token, _ := db.CreateDevice(ctx, "laptop")
	srv := httptest.NewServer(api.New(slog.New(slog.DiscardHandler), api.Options{DB: db}))
	defer srv.Close()

	now := time.Now().Truncate(time.Minute).Add(-20 * time.Minute)
	clock := func() time.Time { return now }
	fa := &flakyAPI{Client: NewClient(srv.URL, token)}
	app := "Code.exe"
	a := &Agent{
		Cfg: testCfg(), Buf: openBuf(t), API: fa, Log: slog.New(slog.DiscardHandler), Now: clock,
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{App: app}, nil }),
	}
	interval := 10 * time.Second
	run := func(minutes int) {
		for i := 0; i < minutes*6; i++ {
			if err := a.SampleOnce(ctx, interval); err != nil {
				t.Fatal(err)
			}
			now = now.Add(interval)
		}
	}

	run(3)
	if _, err := a.flush(ctx); err != nil {
		t.Fatal(err)
	}
	fa.down.Store(true) // 5 minutes offline
	run(5)
	if _, err := a.flush(ctx); err == nil {
		t.Fatal("flush should fail while offline")
	}
	fa.down.Store(false)
	run(2)
	for i := 0; i < 2; i++ { // flush twice: resend must not duplicate
		if _, err := a.flush(ctx); err != nil {
			t.Fatal(err)
		}
	}

	bs, _ := db.BucketsBetween(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if len(bs) != 10 {
		t.Fatalf("got %d buckets, want 10 (one per minute)", len(bs))
	}
	total := 0
	for _, b := range bs {
		total += b.ActiveSeconds
		if b.ActiveSeconds != 60 {
			t.Errorf("bucket %v active=%d, want 60", b.Start, b.ActiveSeconds)
		}
	}
	if total != 600 {
		t.Fatalf("total active %d, want 600", total)
	}
}

func TestEventsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	db := storetest.New(t)
	_, token, _ := db.CreateDevice(ctx, "laptop")
	srv := httptest.NewServer(api.New(slog.New(slog.DiscardHandler), api.Options{DB: db}))
	defer srv.Close()
	defer cancel() // runs first: aborts the long-poll so srv.Close does not wait

	var logBuf safeBuffer
	a := &Agent{
		Cfg: testCfg(), Buf: openBuf(t), API: NewClient(srv.URL, token),
		Log:     slog.New(slog.NewTextHandler(&logBuf, nil)),
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{App: "Code.exe"}, nil }),
	}
	a.init()
	go a.eventLoop(ctx)

	if err := db.CreateEvent(ctx, "reminder.due", map[string]any{"text": "minum air"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logBuf.String(), "event reminder.due") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), `text="minum air"`) {
		t.Fatalf("log = %s", logBuf.String())
	}
	_ = a.SampleOnce(ctx, 10*time.Second)
	if f := a.CurrentForm(); f.Form != forms.Ingat || f.Speech != "minum air" {
		t.Fatalf("form = %+v", f)
	}
	if !strings.Contains(logBuf.String(), "form changed") {
		t.Fatal("form transition not logged")
	}
	// acked + persisted
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, _ := a.Buf.GetKV(ctx, kvLastEventID); v != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("last event id not persisted")
}

func TestHandleEventFreshness(t *testing.T) {
	now := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	a := &Agent{Cfg: testCfg(), Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now }}
	payload, _ := json.Marshal(map[string]string{"text": "lama"})
	a.HandleEvent(Event{ID: 1, Type: "reminder.due", Payload: payload, CreatedAt: now.Add(-7 * time.Hour)})
	a.HandleEvent(Event{ID: 2, Type: "task.done", Payload: []byte(`{}`), CreatedAt: now.Add(-time.Minute)})
	if !a.remindAt.IsZero() || !a.doneAt.IsZero() {
		t.Fatal("stale events must not trigger forms")
	}
	a.HandleEvent(Event{ID: 3, Type: "task.done", Payload: []byte(`{}`), CreatedAt: now.Add(-3 * time.Second)})
	if a.doneAt.IsZero() {
		t.Fatal("fresh task.done ignored")
	}
	a.HandleEvent(Event{ID: 4, Type: "form.set", Payload: []byte(`{"form":"tidur"}`), CreatedAt: now})
	if a.override != forms.Tidur {
		t.Fatal("override not applied")
	}
	a.HandleEvent(Event{ID: 5, Type: "form.set", Payload: []byte(`{"form":"auto"}`), CreatedAt: now})
	if a.override != "" {
		t.Fatal("auto not applied")
	}
}

func TestBackoff(t *testing.T) {
	base := time.Minute
	if backoff(base, 1) != time.Minute || backoff(base, 3) != 4*time.Minute || backoff(base, 20) != maxBackoff {
		t.Fatal("backoff")
	}
}

func TestRotatingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.log")
	r, err := OpenRotatingFile(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := bytes.Repeat([]byte("x"), 40)
	for i := 0; i < 10; i++ {
		_, _ = r.Write(line)
	}
	r.Close()
	st, _ := os.Stat(p)
	st1, err := os.Stat(p + ".1")
	if err != nil || st.Size() > 100 || st1.Size() > 100 {
		t.Fatalf("sizes %v %v %v", st.Size(), st1, err)
	}
}

func TestNoTitleInWireFormat(t *testing.T) {
	b, _ := json.Marshal(Bucket{App: "Code.exe"})
	if strings.Contains(strings.ToLower(string(b)), "title") {
		t.Fatal("wire format must not carry titles")
	}
}

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
