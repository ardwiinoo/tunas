package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstatus"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type notes struct{ msgs []string }

func (n *notes) NotifyOwner(_ context.Context, t string) error {
	n.msgs = append(n.msgs, t)
	return nil
}

func TestAgentOfflineOnlineNotices(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	n := &notes{}
	m := NewMonitor(db, n, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	check := func(step string, want int) {
		t.Helper()
		if err := m.CheckAgent(ctx); err != nil {
			t.Fatal(err)
		}
		if len(n.msgs) != want {
			t.Fatalf("%s: %d messages %q, want %d", step, len(n.msgs), n.msgs, want)
		}
	}

	check("no devices", 0)
	_, tok, _ := db.CreateDevice(ctx, "laptop")
	seen := func() { _, _ = db.AuthDevice(ctx, tok, now) }
	seen() // 10:00

	now = now.Add(15 * time.Minute)
	check("exactly 15 min", 0)
	now = now.Add(time.Minute)
	check("offline after 15 min", 1)
	if n.msgs[0] != "💤 Laptop offline sejak 10:00." {
		t.Fatalf("offline text = %q", n.msgs[0])
	}
	now = now.Add(3 * time.Hour)
	check("still offline: once per outage", 1)

	seen() // back at 13:16
	check("back online", 2)
	if n.msgs[1] != "✅ Laptop kembali online." {
		t.Fatalf("online text = %q", n.msgs[1])
	}
	check("online: nothing new", 2)

	// flapping within the hour: each notice kind at most once per hour
	now = now.Add(20 * time.Minute) // 13:36, last seen 13:16
	check("offline again (offline notice was 3.5 h ago)", 3)
	seen()
	check("online again within the hour of the last online notice: quiet", 3)
	now = now.Add(20 * time.Minute) // 13:56: offline 20 min, offline notice sent 20 min ago
	check("second outage within the hour is quiet", 3)
	seen()
	check("no online notice without an offline one", 3)
	now = now.Add(70 * time.Minute)
	check("after the cooldown the notice goes out", 4)

	// switched off with /settings: no notices at all
	if err := db.SetSetting(ctx, agentstatus.SettingNotify, false); err != nil {
		t.Fatal(err)
	}
	seen()
	check("off: no online notice", 4)
	now = now.Add(2 * time.Hour)
	check("off: no offline notice", 4)

	// revoked devices are ignored
	_ = db.RevokeDevice(ctx, "laptop", now)
	_ = db.SetSetting(ctx, agentstatus.SettingNotify, true)
	check("revoked", 4)
}

type flaky struct{ err error }

func (f *flaky) Complete(context.Context, []llm.Message) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return "ok", nil
}

func TestLLMFailureAlertWithCooldown(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	n := &notes{}
	m := NewMonitor(nil, n, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	inner := &flaky{err: &llm.StatusError{Code: 429}}
	c := m.Watch(inner)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, _ = c.Complete(ctx, nil)
	}
	if len(n.msgs) != 0 {
		t.Fatal("alert before threshold")
	}
	_, _ = c.Complete(ctx, nil)
	_, _ = c.Complete(ctx, nil) // within cooldown
	if len(n.msgs) != 1 || !strings.Contains(n.msgs[0], "gagal 3 kali") || !strings.Contains(n.msgs[0], "429") {
		t.Fatalf("alerts = %q", n.msgs)
	}
	now = now.Add(61 * time.Minute)
	_, _ = c.Complete(ctx, nil)
	if len(n.msgs) != 2 {
		t.Fatal("alert after cooldown")
	}
	inner.err = nil
	_, _ = c.Complete(ctx, nil)
	inner.err = llm.ErrCap
	for i := 0; i < 5; i++ {
		_, _ = c.Complete(ctx, nil)
	}
	now = now.Add(2 * time.Hour)
	inner.err = errors.New("x")
	_, _ = c.Complete(ctx, nil)
	if len(n.msgs) != 2 {
		t.Fatalf("cap errors must not count and success resets: %q", n.msgs)
	}
}

func TestCleanupAndExport(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Minute)
	d, _, _ := db.CreateDevice(ctx, "laptop")
	_ = db.UpsertBuckets(ctx, d.ID, []store.Bucket{
		{Start: now.Add(-200 * 24 * time.Hour), App: "Old.exe", Category: "other", ActiveSeconds: 60},
		{Start: now.Add(-time.Hour), App: "Code.exe", Category: "work", ActiveSeconds: 42},
	})
	_, _ = db.CreateTask(ctx, "beli kopi")
	m := NewMonitor(db, &notes{}, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	bs, _ := db.BucketsBetween(ctx, now.Add(-365*24*time.Hour), now)
	if len(bs) != 1 || bs[0].App != "Code.exe" {
		t.Fatalf("after cleanup = %+v", bs)
	}

	var buf bytes.Buffer
	if err := db.Export(ctx, &buf, now); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tasks           []map[string]any `json:"tasks"`
		Devices         []map[string]any `json:"devices"`
		ActivityBuckets []map[string]any `json:"activity_buckets"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if len(out.Tasks) != 1 || len(out.ActivityBuckets) != 1 || out.ActivityBuckets[0]["active_seconds"].(float64) != 42 {
		t.Fatalf("export = %s", buf.String())
	}
	if strings.Contains(buf.String(), "token_hash") {
		t.Fatal("export must not contain token hashes")
	}
}

func TestModelBlockedAlertOncePerModel(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	n := &syncNotes{}
	m := NewMonitor(nil, n, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	m.ModelBlocked("p/primary", 403)
	m.ModelBlocked("p/primary", 403) // within the cooldown
	m.ModelBlocked("f/one", 401)
	deadline := time.Now().Add(2 * time.Second)
	for n.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a third (duplicate) alert would land by now
	msgs := n.all()
	if len(msgs) != 2 {
		t.Fatalf("alerts = %q", msgs)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "p/primary (HTTP 403)") || !strings.Contains(joined, "f/one (HTTP 401)") ||
		!strings.Contains(joined, "virtual key") {
		t.Fatalf("alerts = %q", msgs)
	}
}

type syncNotes struct {
	mu   sync.Mutex
	msgs []string
}

func (s *syncNotes) NotifyOwner(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, text)
	return nil
}

func (s *syncNotes) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.msgs) }

func (s *syncNotes) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}
