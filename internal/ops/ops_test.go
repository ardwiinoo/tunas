package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type notes struct{ msgs []string }

func (n *notes) NotifyOwner(_ context.Context, t string) error {
	n.msgs = append(n.msgs, t)
	return nil
}

func TestAgentDownOncePerOutage(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	n := &notes{}
	m := NewMonitor(db, n, func() time.Time { return now }, slog.New(slog.DiscardHandler))

	if err := m.CheckAgent(ctx); err != nil || len(n.msgs) != 0 {
		t.Fatal("no devices: no alert")
	}
	_, tok, _ := db.CreateDevice(ctx, "laptop")
	_, _ = db.AuthDevice(ctx, tok, now) // seen now

	now = now.Add(23 * time.Hour)
	_ = m.CheckAgent(ctx)
	if len(n.msgs) != 0 {
		t.Fatal("alert before 24h")
	}
	now = now.Add(2 * time.Hour)
	_ = m.CheckAgent(ctx)
	now = now.Add(3 * time.Hour) // still down, past cooldown: no repeat within the same outage
	_ = m.CheckAgent(ctx)
	if len(n.msgs) != 1 || !strings.Contains(n.msgs[0], "tidak terlihat sejak 25 jam") {
		t.Fatalf("alerts = %q", n.msgs)
	}
	_, _ = db.AuthDevice(ctx, tok, now) // comes back
	_ = m.CheckAgent(ctx)
	if len(n.msgs) != 2 || !strings.Contains(n.msgs[1], "terhubung lagi") {
		t.Fatalf("recovery = %q", n.msgs)
	}
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
	m := NewMonitor(nil, n, func() time.Time { return now }, slog.New(slog.DiscardHandler))
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
	m := NewMonitor(db, &notes{}, func() time.Time { return now }, slog.New(slog.DiscardHandler))
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
