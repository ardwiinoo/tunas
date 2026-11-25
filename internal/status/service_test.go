package status

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/llmstats"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
	"github.com/ardwiinoo/tunas/internal/sysstats"
)

func newService(t *testing.T) (*Service, *time.Time) {
	db := storetest.New(t)
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	svc := &Service{
		DB: db, Server: &sysstats.Collector{DB: db, Version: "main-x", Start: now.Add(-time.Hour), Now: func() time.Time { return now },
			CgroupRoot: t.TempDir()},
		LLM:        &llmstats.Tracker{Store: db, Now: func() time.Time { return now }, Loc: time.UTC},
		LLMEnabled: true, Primary: "p/a", LLMCap: 30, Version: "main-x",
		Now: func() time.Time { return now }, Loc: time.UTC,
	}
	return svc, &now
}

func TestReportFromTheDatabase(t *testing.T) {
	ctx := context.Background()
	svc, now := newService(t)
	_, tok, _ := svc.DB.CreateDevice(ctx, "laptop")
	d, err := svc.DB.AuthDevice(ctx, tok, *now)
	if err != nil {
		t.Fatal(err)
	}
	st := agentstats.Stats{TS: now.Unix(), Version: "main-x", PrivateMB: 30, WorkingMB: 20, Goroutines: 12, SyncAgeS: 5, Overlay: "hidden"}
	raw, _ := st.Marshal()
	if err := svc.DB.SetDeviceStats(ctx, d.ID, raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		_, _ = svc.DB.IncrementLLMUsage(ctx, *now)
	}
	svc.LLM.Record("p/a", true, "", time.Second)
	hostFile := filepath.Join(t.TempDir(), "host.json")
	svc.HostFile = hostFile

	card, err := svc.Report(ctx, ViewAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Memori 30 MB ✅", "🤖 LLM hari ini: 12/30 ✅ · utama 1", "VPS: berkas belum ada", "belum ada cache"} {
		if !strings.Contains(card.Text, want) {
			t.Errorf("missing %q in:\n%s", want, card.Text)
		}
	}
	// the five buttons, the first one repeating the current view
	if len(card.Rows) != 1 || len(card.Rows[0]) != 5 {
		t.Fatalf("keyboard %+v", card.Rows)
	}
	for _, b := range card.Rows[0] {
		if len(b.Data) > 64 || !strings.HasPrefix(b.Data, CallbackPrefix) {
			t.Errorf("callback data %q", b.Data)
		}
	}

	// every button leads to its view; refresh keeps the view
	for data, want := range map[string]string{"st:r:all": "🌱 Tunas", "st:agen": "Working set", "st:vps": "Go heap", "st:llm": "p/a (utama)", "st:24j": "24 jam terakhir", "st:r:llm": "p/a (utama)"} {
		c, ok, err := svc.Callback(ctx, data)
		if err != nil || !ok || !strings.Contains(c.Text, want) {
			t.Errorf("%s: ok=%v err=%v\n%s", data, ok, err, c.Text)
		}
	}
	if _, ok, _ := svc.Callback(ctx, "wl:cal:1"); ok {
		t.Error("someone else's callback was taken")
	}
	if _, ok, err := svc.Callback(ctx, "st:hack"); ok || err == nil {
		t.Error("unknown view must be an error")
	}

	// a fresh host file shows up, a stale one is called out
	good := `{"ts":` + strconv.FormatInt(now.Unix()-30, 10) + `,"uptime_s":1000,"load":[0.3,0.2,0.1],"cpus":1,"mem_total_mb":2048,"mem_used_mb":1000,"mem_avail_mb":900,` +
		`"swap_total_mb":0,"swap_used_mb":0,"disks":[{"mount":"/","used_pct":40,"free_gb":20}],"containers":[{"name":"tunas","status":"running","health":"healthy","mem_mb":38,"mem_limit_mb":128,"cpu_pct":0.1}]}`
	if err := os.WriteFile(hostFile, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, _ := svc.Report(ctx, ViewAll); !strings.Contains(c.Text, "VPS: RAM") {
		t.Errorf("host stats missing:\n%s", c.Text)
	}
	*now = now.Add(6 * time.Minute)
	if c, _ := svc.Report(ctx, ViewAll); !strings.Contains(c.Text, "data VPS basi, cek cron host-stats") {
		t.Errorf("stale host file:\n%s", c.Text)
	}
}

func TestSampleEveryFiveMinutesAndPeaks(t *testing.T) {
	ctx := context.Background()
	svc, now := newService(t)
	sample := func() {
		t.Helper()
		if err := svc.Sample(ctx); err != nil {
			t.Fatal(err)
		}
	}
	sample()
	*now = now.Add(2 * time.Minute)
	sample() // too soon: no second sample
	*now = now.Add(3 * time.Minute)
	sample()
	got, err := svc.DB.StatsSince(ctx, "server", 0, now.Add(-time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("%d server samples (%v), want 2", len(got), err)
	}
	// no host file: no host samples
	if h, _ := svc.DB.StatsSince(ctx, "host", 0, now.Add(-time.Hour)); len(h) != 0 {
		t.Fatalf("%d host samples without a host file", len(h))
	}

	// peaks and averages come from the stored samples
	_, tok, _ := svc.DB.CreateDevice(ctx, "laptop")
	dev, _ := svc.DB.AuthDevice(ctx, tok, *now)
	for i, mem := range []float64{40, 80, 60} {
		s := agentstats.Stats{TS: now.Unix(), PeakPrivMB: mem, PrivateMB: mem, CPUPct: float64(i)}
		if err := svc.DB.AddStatsSample(ctx, now.Add(-time.Duration(i+1)*5*time.Minute), "agent", dev.ID, s); err != nil {
			t.Fatal(err)
		}
	}
	d := Data{Now: *now, Loc: time.UTC, Version: "x"}
	if err := svc.With24h(ctx, &d); err != nil {
		t.Fatal(err)
	}
	var mem, srv *Series
	for i := range d.Last24h {
		switch d.Last24h[i].Label {
		case "Laptop memori":
			mem = &d.Last24h[i]
		case "Server Tunas memori":
			srv = &d.Last24h[i]
		}
	}
	if mem == nil || mem.N != 3 || mem.Max != 80 || mem.Avg != 60 {
		t.Fatalf("agent memory series %+v", mem)
	}
	if srv == nil || srv.N != 2 {
		t.Fatalf("server memory series %+v", srv)
	}
	// samples older than a day are not part of the 24 hours
	if err := svc.DB.AddStatsSample(ctx, now.Add(-30*time.Hour), "agent", dev.ID, agentstats.Stats{TS: 1, PeakPrivMB: 999}); err != nil {
		t.Fatal(err)
	}
	d.Last24h = nil
	_ = svc.With24h(ctx, &d)
	for _, s := range d.Last24h {
		if s.Label == "Laptop memori" && s.Max != 80 {
			t.Fatalf("old sample counted: %+v", s)
		}
	}
}
