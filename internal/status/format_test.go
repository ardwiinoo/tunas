package status

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/llmstats"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/sysstats"
)

func sampleData() Data {
	now := time.Date(2026, 11, 25, 10, 30, 15, 0, time.UTC)
	seen := now.Add(-8 * time.Second)
	return Data{
		Now: now, Loc: time.UTC, Version: "main-86a5e8c",
		Server: sysstats.Server{UptimeS: 3*3600 + 12*60, CgroupOK: true, MemBytes: 38 << 20, MemLimit: 128 << 20, CPUPct: 0.2,
			Goroutines: 31, PoolAcq: 3, PoolMax: 10, DBBytes: 24 << 20, Tables: []store.TableSize{{Name: "activity_buckets", Bytes: 9 << 20}}},
		Laptops: []Laptop{{Name: "Laptop", Seen: &seen, Online: true, Version: "main-86a5e8c", Stats: &agentstats.Stats{
			TS: now.Unix() - 20, Version: "main-86a5e8c", PrivateMB: 54.2, PeakPrivMB: 58, WorkingMB: 28, CPUPct: 0.4, Goroutines: 31,
			SyncAgeS: 8, Overlay: "hidden"}, StatsAt: now.Add(-20 * time.Second)}},
		Host: &sysstats.Host{TS: now.Unix() - 60, CPUs: 1, MemTotalMB: 2048, MemUsedMB: 1200, MemAvailMB: 820, SwapTotal: 1024,
			Load:  [3]float64{0.31, 0.28, 0.25},
			Disks: []sysstats.HostDisk{{Mount: "/", UsedPct: 41, FreeGB: 30}},
			Containers: []sysstats.HostContainer{{Name: "tunas", Status: "running", Health: "healthy", MemMB: 38},
				{Name: "bifrost", Status: "running", MemMB: 210}}},
		LLMEnabled: true, Primary: "groq/a", LLMUsed: 12, LLMCap: 30,
		LLM:     llmstats.Day{Models: map[string]llmstats.Model{"groq/a": {OK: 10, MS: 12000}, "or/b": {OK: 2, Fail: 1, MS: 2000, Kinds: map[string]int{"llm_quota": 1}}}},
		Credits: "Pencarian: 38/1000 kredit bulan ini", Backup: "ok 10:12", Holidays: "cache 2 hari",
	}
}

func TestRenderAllMarksAgainstTargets(t *testing.T) {
	out := sampleData().Render(ViewAll)
	for _, want := range []string{
		"🌱 Tunas · main-86a5e8c · naik 3j12m",
		"💻 Laptop: online (sinkron 8 detik lalu) · agen main-86a5e8c",
		"Memori 54 MB ⚠️ (target ≤ 40 MB tanpa overlay) · CPU 0,4% ✅", // 54 MB is over the 40 MB target
		"Antrean: 0 aktivitas, 0 catatan · overlay: tersembunyi · pelacakan: aktif",
		"🖥 Server Tunas: 38 MB / 128 MB (30%) ✅ · CPU 0,2% · goroutine 31",
		"Database: 24 MB · koneksi 3/10",
		"🗄 VPS: RAM 1,2/2 GB (60%)",
		"disk / 41%",
		"Kontainer: tunas 38 MB · bifrost 210 MB",
		"🤖 LLM hari ini: 12/30 ✅ · utama 10 · cadangan 2 · gagal 1",
		"🔎 Pencarian: 38/1000 kredit bulan ini",
		"💾 Backup worklog: ok 10:12 · libur: cache 2 hari",
		"diperbarui 10:30:15",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMarksFlipAtTheTargets(t *testing.T) {
	d := sampleData()
	d.Laptops[0].Stats.PrivateMB = 39
	d.Laptops[0].Stats.CPUPct = 0.9
	if out := d.Render(ViewAgent); strings.Contains(out, "⚠️") {
		t.Errorf("39 MB and 0.9%% are within target:\n%s", out)
	}
	d.Laptops[0].Stats.CPUPct = 1.0 // target is < 1%
	if !strings.Contains(d.Render(ViewAgent), "CPU 1% ⚠️") {
		t.Error("CPU at 1% misses the < 1% target")
	}
	// the overlay visible raises the memory target by 120 MB
	d.Laptops[0].Stats.PrivateMB, d.Laptops[0].Stats.CPUPct, d.Laptops[0].Stats.Overlay = 150, 0.2, "visible"
	if out := d.Render(ViewAgent); strings.Contains(out, "⚠️") {
		t.Errorf("150 MB with the overlay visible is within 160 MB:\n%s", out)
	}
	d.Laptops[0].Stats.PrivateMB = 170
	if out := d.Render(ViewAgent); !strings.Contains(out, "target ≤ 160 MB dengan overlay") {
		t.Errorf("170 MB:\n%s", out)
	}

	d = sampleData()
	d.Server.MemBytes = 70 << 20
	if out := d.Render(ViewAll); !strings.Contains(out, "70 MB / 128 MB (55%) ⚠️ (target ≤ 60 MB)") {
		t.Errorf("server over target:\n%s", out)
	}
	d = sampleData()
	d.Server.MemBytes = 110 << 20 // over 80% of the limit
	if !strings.Contains(d.Render(ViewAll), "86%) ⚠️") {
		t.Error("server memory over 80% of its limit")
	}
	d = sampleData()
	d.LLMUsed = 25
	if !strings.Contains(d.Render(ViewAll), "25/30 ⚠️") {
		t.Error("LLM use over 80%")
	}
	d = sampleData()
	d.Host.Disks[0].UsedPct = 90
	d.Host.Containers[0].Health = "unhealthy"
	out := d.Render(ViewVPS)
	if !strings.Contains(out, "Perhatian: disk / 90%, kontainer tunas unhealthy") {
		t.Errorf("vps problems:\n%s", out)
	}
	// the owner's thresholds change the marks
	d = sampleData()
	d.T = Thresholds{Enabled: true, ServerMem: 20, VPSRAM: 50, Disk: 30, LLM: 30}
	out = d.Render(ViewAll)
	if !strings.Contains(out, "(30%) ⚠️") || !strings.Contains(out, "12/30 ⚠️") || !strings.Contains(out, "Perhatian: RAM 60%, disk / 41%") {
		t.Errorf("custom thresholds:\n%s", out)
	}
}

func TestHostStatesAreSpelledOut(t *testing.T) {
	for err, want := range map[error]string{
		sysstats.ErrHostOff:   "dimatikan (HOST_STATS_FILE kosong)",
		sysstats.ErrHostStale: "data VPS basi, cek cron host-stats",
		sysstats.ErrHostEmpty: "berkas belum ada",
		sysstats.ErrHostBad:   "tidak terbaca",
	} {
		d := sampleData()
		d.Host, d.HostErr = nil, err
		if out := d.Render(ViewAll); !strings.Contains(out, want) {
			t.Errorf("%v: %s", err, out)
		}
	}
}

func TestDetailViews(t *testing.T) {
	d := sampleData()
	d.Laptops[0].Stats.LastErr = "timeout"
	if out := d.Render(ViewAgent); !strings.Contains(out, "Working set 28 MB") || !strings.Contains(out, "Kegagalan terakhir: timeout") {
		t.Errorf("agen:\n%s", out)
	}
	out := d.Render(ViewLLM)
	for _, want := range []string{"groq/a (utama): 10 berhasil, 0 gagal · rata-rata 1,2 dtk", "or/b (cadangan): 2 berhasil, 1 gagal · rata-rata 1 dtk (llm_quota 1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("llm missing %q:\n%s", want, out)
		}
	}
	out = d.Render(ViewVPS)
	if !strings.Contains(out, "bifrost: 210 MB") || !strings.Contains(out, "tabel activity_buckets: 9 MB") {
		t.Errorf("vps:\n%s", out)
	}
	if out := (Data{Now: d.Now, Loc: time.UTC, Version: "x"}).Render(View24h); !strings.Contains(out, "Belum ada cuplikan") {
		t.Errorf("24j empty:\n%s", out)
	}
	d.Last24h = []Series{{Label: "Server Tunas memori", Unit: " MB", Max: 52.3, Avg: 40.1, N: 288}, {Label: "kosong", N: 0}}
	if out := d.Render(View24h); !strings.Contains(out, "Server Tunas memori: 52,3 MB · 40,1 MB (288 cuplikan)") || strings.Contains(out, "kosong") {
		t.Errorf("24j:\n%s", out)
	}
	// no laptop at all, and an old agent without statistics
	d.Laptops = nil
	if !strings.Contains(d.Render(ViewAll), "belum ada perangkat") {
		t.Error("no device")
	}
	d.Laptops = []Laptop{{Name: "Laptop", Online: true, Seen: &d.Now}}
	if !strings.Contains(d.Render(ViewAll), "Statistik agen belum diterima") {
		t.Error("agent without stats")
	}
}

func TestReportHasNoSecretsOrFreeText(t *testing.T) {
	out := sampleData().Render(ViewAll) + sampleData().Render(ViewVPS)
	for _, banned := range []string{"tnd_", "token", "password", "api_key", "github_"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(banned)) {
			t.Errorf("report contains %q", banned)
		}
	}
	if len(sampleData().Render(ViewAll)) > 3500 { // a Telegram message holds 4096
		t.Error("the main report is too long")
	}
}

func TestUptime(t *testing.T) {
	for s, want := range map[int64]string{59: "0m", 45 * 60: "45m", 3*3600 + 12*60: "3j12m", 50 * 3600: "2h2j"} {
		if got := Uptime(s); got != want {
			t.Errorf("Uptime(%d) = %q, want %q", s, got, want)
		}
	}
}

type memSettings map[string][]byte

func (m memSettings) GetSetting(_ context.Context, k string, dst any) (bool, error) {
	b, ok := m[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, dst)
}

func (m memSettings) SetSetting(_ context.Context, k string, v any) error {
	b, _ := json.Marshal(v)
	m[k] = b
	return nil
}

func TestThresholdSettings(t *testing.T) {
	ctx := context.Background()
	st := memSettings{}
	if got, err := LoadThresholds(ctx, st); err != nil || got != DefaultThresholds() {
		t.Fatalf("defaults %+v %v", got, err)
	}
	if err := SetThreshold(ctx, st, "ram", 70, false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, KeyAlertsOn, false); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadThresholds(ctx, st)
	if got.VPSRAM != 70 || got.Enabled || got.Disk != 85 {
		t.Fatalf("overrides %+v", got)
	}
	if SetThreshold(ctx, st, "ram", 0, false) == nil || SetThreshold(ctx, st, "ram", 101, false) == nil || SetThreshold(ctx, st, "nope", 50, false) == nil {
		t.Fatal("bad thresholds must be refused")
	}
	if err := SetThreshold(ctx, st, "ram", 0, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadThresholds(ctx, st); got.VPSRAM != 85 {
		t.Fatalf("reset %+v", got)
	}
}
