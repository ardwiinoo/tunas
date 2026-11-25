package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
)

func TestLocalStatusTextMarks(t *testing.T) {
	s := agentstats.Stats{TS: 1, Version: "main-86a5e8c", UptimeS: 3*3600 + 12*60, PrivateMB: 54.2, CPUPct: 0.4, QueueWL: 2, SyncAgeS: 8, Overlay: "hidden"}
	out := LocalStatusText(s)
	for _, want := range []string{"agen main-86a5e8c, naik 3j12m", "Memori 54 MB ! (target 40 MB) · CPU 0,4% √", "Antrean: 0 aktivitas, 2 catatan · overlay: tersembunyi · pelacakan: aktif", "Sinkron terakhir 8 detik lalu"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	s.Overlay, s.PrivateMB, s.Paused, s.SyncAgeS = "visible", 150, true, -1
	out = LocalStatusText(s)
	if !strings.Contains(out, "Memori 150 MB √ (target 160 MB)") || !strings.Contains(out, "pelacakan: dijeda") || !strings.Contains(out, "Belum pernah sinkron") {
		t.Errorf("overlay visible:\n%s", out)
	}
	// the bubble font cannot draw emoji or a check mark
	for _, r := range out {
		if r > 0x2000 && r != '√' && r != '·' {
			t.Errorf("rune %q (%U) is not drawable in the bubble", r, r)
		}
	}
}

type fakeSummary struct {
	text string
	err  error
}

func (f fakeSummary) StatusSummary(context.Context) (string, error) { return f.text, f.err }

func TestStatusReportCombinesLocalAndServer(t *testing.T) {
	ctx := context.Background()
	fp := &fakeProc{s: ProcSample{PrivateBytes: 30 << 20, WorkingSet: 20 << 20}}
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler), Version: "v", Proc: fp}
	a.init()
	out := a.StatusReport(ctx, fakeSummary{text: "Server Tunas: 38 MB √"})
	if !strings.Contains(out, "Memori 30 MB √") || !strings.Contains(out, "Server Tunas: 38 MB √") {
		t.Fatalf("report:\n%s", out)
	}
	out = a.StatusReport(ctx, fakeSummary{err: errors.New("dial tcp 10.0.0.1: refused")})
	if !strings.Contains(out, "Memori 30 MB") || !strings.Contains(out, "Server tidak terjangkau") || strings.Contains(out, "10.0.0.1") {
		t.Fatalf("offline report (no error text may leak):\n%s", out)
	}
}

func TestStatsFileForStatusFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	if _, err := ReadStatsFile(path, now); err == nil || !strings.Contains(err.Error(), "belum berjalan") {
		t.Fatalf("missing file: %v", err)
	}
	s := agentstats.Stats{TS: now.Unix() - 30, Version: "main-x", PrivateMB: 41, Overlay: "off", SyncAgeS: 3}
	if err := WriteStatsFile(path, s); err != nil {
		t.Fatal(err)
	}
	out, err := ReadStatsFile(path, now)
	if err != nil || !strings.Contains(string(out), `"priv_mb": 41`) {
		t.Fatalf("%s %v", out, err)
	}
	// nothing but the fixed numbers: no token, no config
	for _, banned := range []string{"token", "tnd_", "server_url", "http"} {
		if strings.Contains(strings.ToLower(string(out)), banned) {
			t.Errorf("--status output has %q", banned)
		}
	}
	if _, err := ReadStatsFile(path, now.Add(10*time.Minute)); err == nil || !strings.Contains(err.Error(), "tidak berjalan") {
		t.Fatalf("a stale file means the agent is not running: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"ts":1,"title":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatsFile(path, now); err == nil {
		t.Fatal("a file with extra fields must be refused")
	}
}

func TestSampleWritesTheStatsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.json")
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler), Version: "v", StatsFile: path,
		Proc: &fakeProc{s: ProcSample{PrivateBytes: 10 << 20}}}
	a.init()
	a.SampleStats(context.Background())
	if _, err := ReadStatsFile(path, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("the temporary file is renamed away")
	}
}
