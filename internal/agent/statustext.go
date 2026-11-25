package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
)

// Memory and CPU targets of PRD section 9 (the server's /status uses the
// same numbers; the agent cannot import that package, which needs Postgres).
const (
	targetMemMB        = 40.0
	targetMemOverlayMB = 160.0
	targetCPUPct       = 1.0
)

func num1(f float64, digits int) string {
	s := fmt.Sprintf("%.*f", digits, f)
	if digits > 0 {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.Replace(s, ".", ",", 1)
}

func ok(b bool) string {
	if b {
		return "√" // the bubble font has no emoji or check mark
	}
	return "!"
}

var overlayWords = map[string]string{"off": "mati", "hidden": "tersembunyi", "visible": "terlihat", "": "?"}

// LocalStatusText is the agent's own part of /status in the bubble and the
// tray's status card: plain text, no emoji.
func LocalStatusText(s agentstats.Stats) string {
	target := targetMemMB
	if s.Overlay == "visible" {
		target = targetMemOverlayMB
	}
	up := time.Duration(s.UptimeS) * time.Second
	track := "aktif"
	if s.Paused {
		track = "dijeda"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Laptop: agen %s, naik %dj%dm", s.Version, int(up.Hours()), int(up.Minutes())%60)
	fmt.Fprintf(&b, "\nMemori %s MB %s (target %s MB) · CPU %s%% %s",
		num1(s.PrivateMB, 0), ok(s.PrivateMB <= target), num1(target, 0), num1(s.CPUPct, 1), ok(s.CPUPct < targetCPUPct))
	fmt.Fprintf(&b, "\nAntrean: %d aktivitas, %d catatan · overlay: %s · pelacakan: %s", s.QueueAct, s.QueueWL, overlayWords[s.Overlay], track)
	if s.SyncAgeS >= 0 {
		fmt.Fprintf(&b, "\nSinkron terakhir %d detik lalu", s.SyncAgeS)
	} else {
		b.WriteString("\nBelum pernah sinkron")
	}
	return b.String()
}

// StatusSummary fetches the server, VPS and LLM part of /status.
func (c *Client) StatusSummary(ctx context.Context) (string, error) {
	var out struct {
		Text string `json:"text"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/agent/status/summary", nil, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

// StatusReport is the card of the tray's "Status…" and "/status" in the
// bubble: live local numbers, then the server's summary (or why it is
// missing).
func (a *Agent) StatusReport(ctx context.Context, api interface {
	StatusSummary(context.Context) (string, error)
}) string {
	a.SampleStats(ctx)
	var parts []string
	if s, ok := a.Stats(); ok {
		parts = append(parts, LocalStatusText(s))
	} else {
		parts = append(parts, "Statistik agen belum tersedia.")
	}
	if api != nil {
		if text, err := api.StatusSummary(ctx); err == nil && text != "" {
			parts = append(parts, text)
		} else {
			parts = append(parts, "Server tidak terjangkau, hanya angka laptop.")
		}
	}
	return strings.Join(parts, "\n")
}

// StatsFileMaxAge is how old the local statistics file may be before the
// agent counts as not running.
const StatsFileMaxAge = 3 * time.Minute

// WriteStatsFile stores the latest sample next to the other local data (an
// atomic write), so `tunas-agent.exe --status` can show the numbers of the
// running agent without any network call or token.
func WriteStatsFile(path string, s agentstats.Stats) error {
	raw, err := s.Marshal()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadStatsFile is what `--status` prints: the running agent's last sample
// as JSON, or an error when the file is missing or too old.
func ReadStatsFile(path string, now time.Time) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agen belum berjalan (belum ada %s)", filepath.Base(path))
	}
	s, err := agentstats.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("berkas statistik rusak: %w", err)
	}
	if age := now.Sub(time.Unix(s.TS, 0)); age > StatsFileMaxAge {
		return nil, fmt.Errorf("agen tampaknya tidak berjalan (data terakhir %d menit lalu)", int(age.Minutes()))
	}
	return json.MarshalIndent(s, "", "  ")
}
