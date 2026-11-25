package ops

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/status"
	"github.com/ardwiinoo/tunas/internal/sysstats"
)

type fakeRes struct {
	d       status.Data
	sampled int
}

func (f *fakeRes) Gather(context.Context) (status.Data, error) { return f.d, nil }
func (f *fakeRes) Sample(context.Context) error                { f.sampled++; return nil }

func healthy() status.Data {
	return status.Data{
		Server: sysstats.Server{MemBytes: 38 << 20, MemLimit: 128 << 20},
		Host: &sysstats.Host{MemTotalMB: 2000, MemAvailMB: 800,
			Disks:      []sysstats.HostDisk{{Mount: "/", UsedPct: 41, FreeGB: 20}},
			Containers: []sysstats.HostContainer{{Name: "tunas", Status: "running", Health: "healthy"}}},
		LLMEnabled: true, LLMCap: 30, LLMUsed: 12,
	}
}

func TestResourceAlertsThresholdsAndCooldown(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	n := &notes{}
	res := &fakeRes{d: healthy()}
	m := NewMonitor(nil, n, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	m.Res = res
	check := func(step string, want int) {
		t.Helper()
		if err := m.CheckResources(ctx); err != nil {
			t.Fatal(err)
		}
		if len(n.msgs) != want {
			t.Fatalf("%s: %d messages %q, want %d", step, len(n.msgs), n.msgs, want)
		}
	}
	check("healthy", 0)
	if res.sampled != 1 {
		t.Fatal("each check also takes the 5-minute sample")
	}

	// exactly at the threshold is fine, above it alerts once per hour
	res.d.Server.MemBytes = 102 << 20 // 79.7% of 128 MB
	check("79.7%", 0)
	res.d.Server.MemBytes = 110 << 20 // 85.9%
	check("above 80%", 1)
	now = now.Add(30 * time.Minute)
	check("cooldown", 1)
	now = now.Add(31 * time.Minute)
	check("after the cooldown", 2)
	res.d.Server.MemBytes = 38 << 20

	res.d.Host.MemAvailMB = 200 // 90% used
	res.d.Host.Disks[0].UsedPct = 90
	res.d.Host.Containers[0].Health = "unhealthy"
	res.d.LLMUsed = 25 // 83%
	check("vps ram, disk, container, llm", 6)
	joined := strings.Join(n.msgs[2:], "\n")
	for _, want := range []string{"RAM VPS", "Disk /", "tunas tidak sehat", "LLM hari ini 25/30"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}

	// the owner's thresholds win, and alerts can be switched off
	now = now.Add(2 * time.Hour)
	res.d.T = status.Thresholds{Enabled: true, ServerMem: 80, VPSRAM: 95, Disk: 95, LLM: 95}
	check("raised thresholds: containers only", 7)
	now = now.Add(2 * time.Hour)
	res.d.T.Enabled = false
	check("switched off", 7)
}

func TestAgentMemoryMustStayHighForTenMinutes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	n := &notes{}
	d := healthy()
	d.Laptops = []status.Laptop{{Name: "Laptop", Online: true, Stats: &agentstats.Stats{TS: 1, PrivateMB: 90, Overlay: "hidden"}}}
	res := &fakeRes{d: d}
	m := NewMonitor(nil, n, func() time.Time { return now }, time.UTC, slog.New(slog.DiscardHandler))
	m.Res = res
	step := func(d time.Duration, want int) {
		t.Helper()
		now = now.Add(d)
		if err := m.CheckResources(ctx); err != nil || len(n.msgs) != want {
			t.Fatalf("%v: %d messages %q, want %d (%v)", d, len(n.msgs), n.msgs, want, err)
		}
	}
	step(0, 0)             // over 2x40 MB: the clock starts
	step(9*time.Minute, 0) // not yet
	res.d.Laptops[0].Stats.PrivateMB = 60
	step(time.Minute, 0) // dipped: the clock resets
	res.d.Laptops[0].Stats.PrivateMB = 90
	step(time.Minute, 0)
	step(10*time.Minute, 1) // ten minutes again
	if !strings.Contains(n.msgs[0], "Memori agen Laptop 90 MB") {
		t.Fatalf("text %q", n.msgs[0])
	}
	// with the overlay visible the target is 160 MB, so 90 MB is nothing
	now = now.Add(2 * time.Hour)
	res.d.Laptops[0].Stats.Overlay = "visible"
	step(0, 1)
	step(20*time.Minute, 1)
}
