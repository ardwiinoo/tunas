// Package status builds the /status report (PRD Fase 8g): laptop agent,
// Tunas server and VPS resource use against the targets of PRD section 9,
// LLM use, search credits, backup and holiday cache. format.go is pure text
// (testable); service.go gathers the numbers.
package status

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/agentstatus"
	"github.com/ardwiinoo/tunas/internal/llmstats"
	"github.com/ardwiinoo/tunas/internal/sysstats"
)

// Targets (PRD section 9) and alert thresholds.
const (
	TargetServerMB       = 60.0  // container RSS idle
	TargetAgentMB        = 40.0  // agent without overlay
	TargetOverlayExtraMB = 120.0 // extra while the overlay is visible
	TargetAgentCPU       = 1.0   // percent
	ServerMemPctMax      = 80.0  // of the container limit
	VPSRAMPctMax         = 85.0
	DiskPctMax           = 85
	LLMUsePctMax         = 80.0
)

// Views of the report.
const (
	ViewAll   = ""
	ViewAgent = "agen"
	ViewVPS   = "vps"
	ViewLLM   = "llm"
	View24h   = "24j"
)

// ValidView reports whether v is a known view.
func ValidView(v string) bool {
	switch v {
	case ViewAll, ViewAgent, ViewVPS, ViewLLM, View24h:
		return true
	}
	return false
}

// Laptop is one device as shown in the report.
type Laptop struct {
	Name    string
	Seen    *time.Time // last contact
	Online  bool
	Version string
	Paused  bool
	Pending int // events waiting on the server
	Stats   *agentstats.Stats
	StatsAt time.Time // when the server received Stats
}

// Series is one metric's peak and average over the last 24 hours.
type Series struct {
	Label string
	Unit  string // "MB", "%"
	Max   float64
	Avg   float64
	N     int
}

// Data is everything a report needs.
type Data struct {
	Now     time.Time
	Loc     *time.Location
	Version string
	Server  sysstats.Server
	Laptops []Laptop

	Host    *sysstats.Host
	HostErr error // ErrHostOff | ErrHostStale | ErrHostBad | ErrHostEmpty

	LLMEnabled bool
	LLM        llmstats.Day
	Primary    string
	LLMUsed    int // requests counted against the daily cap
	LLMCap     int

	Credits  string // "38/1000 kredit bulan ini ..." ("" = search off)
	Backup   string // "ok 10:12" ...
	Holidays string // "cache 2 hari" ...

	Last24h []Series

	T Thresholds // zero = defaults
}

func (d Data) thr() Thresholds {
	if d.T == (Thresholds{}) {
		return DefaultThresholds()
	}
	return d.T
}

// Mark is ✅ when ok, else ⚠️.
func Mark(ok bool) string {
	if ok {
		return "✅"
	}
	return "⚠️"
}

// num formats with an Indonesian decimal comma; whole numbers have no fraction.
func num(f float64, digits int) string {
	s := fmt.Sprintf("%.*f", digits, f)
	if digits > 0 && strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.Replace(s, ".", ",", 1)
}

func gb(mb int64) string { return num(float64(mb)/1024, 1) }

// Uptime formats seconds as "3j12m", "2h4j" or "45m".
func Uptime(s int64) string {
	d := time.Duration(s) * time.Second
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dh%dj", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dj%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// AgentMemTarget is the memory target of an agent in a given overlay state.
func AgentMemTarget(overlay string) float64 {
	if overlay == "visible" {
		return TargetAgentMB + TargetOverlayExtraMB
	}
	return TargetAgentMB
}

func (l Laptop) head(now time.Time, loc *time.Location) string {
	switch {
	case l.Seen == nil:
		return "belum pernah tersambung"
	case l.Online:
		s := "online"
		if l.Stats != nil && l.Stats.SyncAgeS >= 0 {
			s += fmt.Sprintf(" (sinkron %s lalu)", agentstatus.Ago(time.Duration(l.Stats.SyncAgeS)*time.Second))
		} else {
			s += fmt.Sprintf(" (terakhir terlihat %s lalu)", agentstatus.Ago(now.Sub(*l.Seen)))
		}
		return s
	}
	return fmt.Sprintf("offline sejak %s (%s)", agentstatus.Clock(*l.Seen, now, loc), agentstatus.Ago(now.Sub(*l.Seen)))
}

var overlayWords = map[string]string{"off": "mati", "hidden": "tersembunyi", "visible": "terlihat", "": "?"}

var errWords = map[string]string{"network": "jaringan", "timeout": "timeout", "auth": "token ditolak", "server": "server error", "other": "lainnya"}

func (d Data) laptopLines(detail bool) []string {
	if len(d.Laptops) == 0 {
		return []string{"💻 Laptop: belum ada perangkat (tunas-server device create --name laptop)"}
	}
	var out []string
	for _, l := range d.Laptops {
		v := l.Version
		if v == "" {
			v = "?"
		}
		out = append(out, fmt.Sprintf("💻 %s: %s · agen %s", l.Name, l.head(d.Now, d.Loc), v))
		if l.Stats == nil {
			out = append(out, "   Statistik agen belum diterima (agen lama atau belum heartbeat).")
			continue
		}
		s := *l.Stats
		target := AgentMemTarget(s.Overlay)
		memOK := s.PrivateMB <= target
		cpuOK := s.CPUPct < TargetAgentCPU
		note := ""
		if !memOK {
			note = fmt.Sprintf(" (target ≤ %s MB%s)", num(target, 0), map[bool]string{true: " dengan overlay", false: " tanpa overlay"}[s.Overlay == "visible"])
		}
		out = append(out, fmt.Sprintf("   Memori %s MB %s%s · CPU %s%% %s", num(s.PrivateMB, 0), Mark(memOK), note, num(s.CPUPct, 1), Mark(cpuOK)))
		track := "aktif"
		if s.Paused || l.Paused {
			track = "dijeda"
		}
		out = append(out, fmt.Sprintf("   Antrean: %d aktivitas, %d catatan · overlay: %s · pelacakan: %s",
			s.QueueAct, s.QueueWL, overlayWords[s.Overlay], track))
		if detail {
			out = append(out, fmt.Sprintf("   Working set %s MB · puncak %s MB · goroutine %d · naik %s",
				num(s.WorkingMB, 0), num(s.PeakPrivMB, 0), s.Goroutines, Uptime(s.UptimeS)))
			if s.LastErr != "" {
				out = append(out, "   Kegagalan terakhir: "+errWords[s.LastErr])
			}
			out = append(out, fmt.Sprintf("   Event tertunda di server: %d · statistik diterima %s lalu",
				l.Pending, agentstatus.Ago(d.Now.Sub(l.StatsAt))))
		}
	}
	return out
}

func (d Data) serverLines(detail bool) []string {
	s := d.Server
	memMB := float64(s.MemBytes) / (1 << 20)
	ok := memMB <= TargetServerMB && (s.MemLimit <= 0 || s.MemPct() <= d.thr().ServerMem)
	line := fmt.Sprintf("🖥 Server Tunas: %s MB", num(memMB, 0))
	if s.MemLimit > 0 {
		line += fmt.Sprintf(" / %s MB (%s%%)", num(float64(s.MemLimit)/(1<<20), 0), num(s.MemPct(), 0))
	}
	line += " " + Mark(ok)
	if !ok {
		line += fmt.Sprintf(" (target ≤ %s MB)", num(TargetServerMB, 0))
	}
	if !s.CgroupOK {
		line += " · cgroup tidak terbaca, ini memori Go"
	}
	line += fmt.Sprintf(" · CPU %s%% · goroutine %d", num(s.CPUPct, 1), s.Goroutines)
	out := []string{line}
	if s.PoolMax > 0 || s.DBBytes > 0 {
		out = append(out, fmt.Sprintf("   Database: %s MB · koneksi %d/%d", num(float64(s.DBBytes)/(1<<20), 0), s.PoolAcq, s.PoolMax))
	}
	if detail {
		out = append(out, fmt.Sprintf("   Go heap %s MB · Go sys %s MB · naik %s · versi %s",
			num(s.GoHeapMB, 1), num(s.GoSysMB, 1), Uptime(s.UptimeS), s.Version))
		for _, t := range s.Tables {
			out = append(out, fmt.Sprintf("   tabel %s: %s MB", t.Name, num(float64(t.Bytes)/(1<<20), 1)))
		}
	}
	return out
}

// HostProblems lists what is wrong on the VPS (empty = fine). Used by the
// report marks and by the alerts.
func HostProblems(h sysstats.Host, t Thresholds) []string {
	var p []string
	if h.MemPct() > t.VPSRAM {
		p = append(p, fmt.Sprintf("RAM %s%%", num(h.MemPct(), 0)))
	}
	for _, dk := range h.Disks {
		if float64(dk.UsedPct) > t.Disk {
			p = append(p, fmt.Sprintf("disk %s %d%%", dk.Mount, dk.UsedPct))
		}
	}
	for _, c := range h.Containers {
		if c.Health == "unhealthy" || (c.Status != "" && c.Status != "running") {
			p = append(p, fmt.Sprintf("kontainer %s %s", c.Name, firstNonEmpty(c.Health, c.Status)))
		}
	}
	return p
}

func firstNonEmpty(a, b string) string {
	if a == "unhealthy" || b == "" {
		return a
	}
	return b
}

func (d Data) vpsLines(detail bool) []string {
	if d.HostErr != nil || d.Host == nil {
		msg := "data VPS tidak tersedia"
		switch {
		case errors.Is(d.HostErr, sysstats.ErrHostOff):
			msg = "dimatikan (HOST_STATS_FILE kosong)"
		case errors.Is(d.HostErr, sysstats.ErrHostStale):
			msg = "data VPS basi, cek cron host-stats"
		case errors.Is(d.HostErr, sysstats.ErrHostEmpty):
			msg = "berkas belum ada, pasang cron host-stats"
		case errors.Is(d.HostErr, sysstats.ErrHostBad):
			msg = "berkas host.json tidak terbaca atau bentuknya salah"
		}
		return []string{"🗄 VPS: " + msg + " ⚠️"}
	}
	h := *d.Host
	problems := HostProblems(h, d.thr())
	line := fmt.Sprintf("🗄 VPS: RAM %s/%s GB (%s%%) · swap %s MB", gb(h.MemTotalMB-h.MemAvailMB), gb(h.MemTotalMB), num(h.MemPct(), 0), num(float64(h.SwapUsedMB), 0))
	for _, dk := range h.Disks {
		line += fmt.Sprintf(" · disk %s %d%%", dk.Mount, dk.UsedPct)
	}
	line += fmt.Sprintf(" · beban %s %s %s %s", num(h.Load[0], 2), num(h.Load[1], 2), num(h.Load[2], 2), Mark(len(problems) == 0))
	out := []string{line}
	if len(problems) > 0 {
		out = append(out, "   Perhatian: "+strings.Join(problems, ", "))
	}
	var cs []string
	for _, c := range h.Containers {
		cs = append(cs, fmt.Sprintf("%s %s MB", c.Name, num(c.MemMB, 0)))
	}
	if len(cs) > 0 {
		out = append(out, "   Kontainer: "+strings.Join(cs, " · "))
	}
	if detail {
		out = append(out, fmt.Sprintf("   %d CPU · naik %s · data %s lalu", h.CPUs, Uptime(h.UptimeS), agentstatus.Ago(d.Now.Sub(time.Unix(h.TS, 0)))))
		for _, dk := range h.Disks {
			out = append(out, fmt.Sprintf("   disk %s: %d%% terpakai, sisa %s GB", dk.Mount, dk.UsedPct, num(dk.FreeGB, 1)))
		}
		for _, c := range h.Containers {
			hl := ""
			if c.Health != "" {
				hl = " · " + c.Health
			}
			lim := ""
			if c.MemLimitMB > 0 {
				lim = " / " + num(c.MemLimitMB, 0)
			}
			out = append(out, fmt.Sprintf("   %s: %s%s MB · CPU %s%% · %s%s", c.Name, num(c.MemMB, 0), lim, num(c.CPUPct, 1), c.Status, hl))
		}
	}
	return out
}

// LLMUsePct is today's use of the daily cap (0 without a cap).
func (d Data) LLMUsePct() float64 {
	if d.LLMCap <= 0 {
		return 0
	}
	return 100 * float64(d.LLMUsed) / float64(d.LLMCap)
}

func (d Data) llmLines(detail bool) []string {
	if !d.LLMEnabled {
		return []string{"🤖 LLM: nonaktif (BIFROST_API_KEY / LLM_PRIMARY_MODEL belum diisi)"}
	}
	p, f, fail := d.LLM.Totals(d.Primary)
	line := fmt.Sprintf("🤖 LLM hari ini: %d/%d %s · utama %d · cadangan %d · gagal %d",
		d.LLMUsed, d.LLMCap, Mark(d.LLMUsePct() <= d.thr().LLM), p, f, fail)
	out := []string{line}
	if detail {
		names := d.LLM.Names(d.Primary)
		if len(names) == 0 {
			out = append(out, "   Belum ada panggilan model hari ini.")
		}
		for _, n := range names {
			m := d.LLM.Models[n]
			role := "cadangan"
			if n == d.Primary {
				role = "utama"
			}
			s := fmt.Sprintf("   %s (%s): %d berhasil, %d gagal", n, role, m.OK, m.Fail)
			if m.OK > 0 {
				s += fmt.Sprintf(" · rata-rata %s dtk", num(float64(m.MS)/float64(m.OK)/1000, 1))
			}
			if len(m.Kinds) > 0 {
				var ks []string
				for k, c := range m.Kinds {
					ks = append(ks, fmt.Sprintf("%s %d", k, c))
				}
				sort.Strings(ks)
				s += " (" + strings.Join(ks, ", ") + ")"
			}
			out = append(out, s)
		}
		out = append(out, "   Hitungan batas harian memuat semua permintaan; percobaan model dicatat per percobaan.")
	}
	return out
}

func (d Data) otherLines() []string {
	var out []string
	if d.Credits != "" {
		out = append(out, "🔎 "+d.Credits)
	}
	var tail []string
	if d.Backup != "" {
		tail = append(tail, "💾 Backup worklog: "+d.Backup)
	}
	if d.Holidays != "" {
		tail = append(tail, "libur: "+d.Holidays)
	}
	if len(tail) > 0 {
		out = append(out, strings.Join(tail, " · "))
	}
	return out
}

func (d Data) seriesLines() []string {
	out := []string{"📈 24 jam terakhir (puncak · rata-rata)"}
	n := 0
	for _, s := range d.Last24h {
		if s.N == 0 {
			continue
		}
		n++
		out = append(out, fmt.Sprintf("   %s: %s%s · %s%s (%d cuplikan)", s.Label, num(s.Max, 1), s.Unit, num(s.Avg, 1), s.Unit, s.N))
	}
	if n == 0 {
		out = append(out, "   Belum ada cuplikan (dicuplik tiap 5 menit sejak server naik).")
	}
	return out
}

// Render builds the text of a view.
func (d Data) Render(view string) string {
	var lines []string
	head := fmt.Sprintf("🌱 Tunas · %s · naik %s", d.Version, Uptime(d.Server.UptimeS))
	switch view {
	case ViewAgent:
		lines = append([]string{head}, d.laptopLines(true)...)
	case ViewVPS:
		lines = append([]string{head}, d.vpsLines(true)...)
		lines = append(lines, d.serverLines(true)...)
	case ViewLLM:
		lines = append([]string{head}, d.llmLines(true)...)
		lines = append(lines, d.otherLines()...)
	case View24h:
		lines = append([]string{head}, d.seriesLines()...)
	default:
		lines = append([]string{head}, d.laptopLines(false)...)
		lines = append(lines, d.serverLines(false)...)
		lines = append(lines, d.vpsLines(false)...)
		lines = append(lines, d.llmLines(false)...)
		lines = append(lines, d.otherLines()...)
	}
	lines = append(lines, "diperbarui "+d.Now.In(d.Loc).Format("15:04:05"))
	return strings.Join(lines, "\n")
}
