package bot

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestSettingsAmbangAndAlertSwitch(t *testing.T) {
	db := storetest.New(t)
	c := &Commands{DB: db, Loc: time.UTC, Now: time.Now, Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()

	if got := c.Handle(ctx, "/settings ambang"); !strings.Contains(got, "ram: 85") || !strings.Contains(got, "server: 80") || !strings.Contains(got, "llm: 80") {
		t.Fatalf("defaults: %q", got)
	}
	if got := c.Handle(ctx, "/settings ambang ram 60"); !strings.Contains(got, "Ambang ram sekarang 60%") {
		t.Fatalf("set: %q", got)
	}
	if got := c.Handle(ctx, "/settings ambang"); !strings.Contains(got, "ram: 60") {
		t.Fatalf("shown: %q", got)
	}
	for _, bad := range []string{"/settings ambang ram 0", "/settings ambang ram 150", "/settings ambang ram abc", "/settings ambang cpu 50", "/settings ambang ram"} {
		if got := c.Handle(ctx, bad); !strings.Contains(got, "Pakai: /settings ambang") {
			t.Errorf("%s: %q", bad, got)
		}
	}
	if got := c.Handle(ctx, "/settings ambang ram reset"); !strings.Contains(got, "kembali ke bawaan") {
		t.Fatalf("reset: %q", got)
	}
	if got := c.Handle(ctx, "/settings ambang"); !strings.Contains(got, "ram: 85") {
		t.Fatalf("after reset: %q", got)
	}

	if got := c.Handle(ctx, "/settings"); !strings.Contains(got, "notif_sumber: on") {
		t.Fatalf("list: %q", got)
	}
	if got := c.Handle(ctx, "/settings notif_sumber off"); !strings.Contains(got, "notif_sumber sekarang off") {
		t.Fatalf("switch: %q", got)
	}
}
