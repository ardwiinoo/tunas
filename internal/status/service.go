package status

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
	"github.com/ardwiinoo/tunas/internal/agentstatus"
	"github.com/ardwiinoo/tunas/internal/llmstats"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/sysstats"
)

// SampleEvery is how often the server and host are sampled into stats_samples.
const SampleEvery = 5 * time.Minute

// Service gathers the numbers of a report.
type Service struct {
	DB       *store.DB
	Server   *sysstats.Collector
	HostFile string // "" = VPS part off
	LLM      *llmstats.Tracker

	LLMEnabled bool
	Primary    string
	LLMCap     int
	Credits    func(ctx context.Context) string // "" when search is off

	Version string
	Now     func() time.Time
	Loc     *time.Location
}

// Gather collects the live numbers.
func (s *Service) Gather(ctx context.Context) (Data, error) {
	now := s.Now()
	d := Data{Now: now, Loc: s.Loc, Version: s.Version, Server: s.Server.Snapshot(ctx),
		LLMEnabled: s.LLMEnabled, Primary: s.Primary, LLMCap: s.LLMCap}
	var err error
	if d.T, err = LoadThresholds(ctx, s.DB); err != nil {
		return d, err
	}
	devs, err := s.DB.ListDevices(ctx)
	if err != nil {
		return d, err
	}
	for _, dev := range devs {
		if dev.RevokedAt != nil {
			continue
		}
		pending, err := s.DB.PendingEvents(ctx, dev.ID)
		if err != nil {
			return d, err
		}
		l := Laptop{Name: agentstatus.Name(dev), Seen: dev.LastSeenAt, Online: agentstatus.Online(dev, now),
			Version: dev.AgentVersion, Paused: dev.TrackingPaused, Pending: pending}
		if raw, err := s.DB.DeviceStats(ctx, dev.ID); err == nil && len(raw) > 0 {
			if st, err := agentstats.Parse(raw); err == nil {
				l.Stats, l.StatsAt = &st, time.Unix(st.TS, 0)
			}
		}
		d.Laptops = append(d.Laptops, l)
	}
	if s.HostFile == "" {
		d.HostErr = sysstats.ErrHostOff
	} else if h, err := sysstats.ReadHost(s.HostFile, now); err != nil {
		d.HostErr = err
	} else {
		d.Host = &h
	}
	if s.LLM != nil {
		d.LLM = s.LLM.Today(ctx)
	}
	if d.LLMUsed, err = s.DB.LLMUsageOn(ctx, now.In(s.Loc)); err != nil {
		return d, err
	}
	if s.Credits != nil {
		d.Credits = s.Credits(ctx)
	}
	d.Backup = s.backupLine(ctx)
	d.Holidays = s.holidayLine(ctx)
	return d, nil
}

func (s *Service) backupLine(ctx context.Context) string {
	sts, err := s.DB.SyncStates(ctx)
	if err != nil || len(sts) == 0 {
		return ""
	}
	var last time.Time
	bad := ""
	for _, st := range sts {
		if st.LastSyncedAt != nil && st.LastSyncedAt.After(last) {
			last = *st.LastSyncedAt
		}
		if st.Status == "conflict" || st.Status == "error" {
			bad = st.Month + " " + st.Status
		}
	}
	out := "belum pernah"
	if !last.IsZero() {
		out = "ok " + agentstatus.Clock(last, s.Now(), s.Loc)
	}
	if bad != "" {
		out += " ⚠️ " + bad
	}
	return out
}

func (s *Service) holidayLine(ctx context.Context) string {
	t, err := s.DB.HolidaysFetchedAt(ctx, s.Now().In(s.Loc).Year())
	if err != nil {
		return ""
	}
	if t.IsZero() {
		return "belum ada cache"
	}
	return "cache " + agentstatus.Ago(s.Now().Sub(t))
}

type pick struct {
	label, unit string
	scope       string
	dev         int64
	f           func(raw json.RawMessage) (float64, bool)
}

func decode[T any](get func(T) float64) func(json.RawMessage) (float64, bool) {
	return func(r json.RawMessage) (float64, bool) {
		var v T
		if json.Unmarshal(r, &v) != nil {
			return 0, false
		}
		return get(v), true
	}
}

// With24h adds the 24-hour peaks and averages from stats_samples.
func (s *Service) With24h(ctx context.Context, d *Data) error {
	since := d.Now.Add(-24 * time.Hour)
	picks := []pick{
		{"Server Tunas memori", " MB", "server", 0, decode(func(v sysstats.Server) float64 { return float64(v.MemBytes) / (1 << 20) })},
		{"Server Tunas CPU", "%", "server", 0, decode(func(v sysstats.Server) float64 { return v.CPUPct })},
		{"VPS RAM", "%", "host", 0, decode(func(v sysstats.Host) float64 { return v.MemPct() })},
	}
	devs, err := s.DB.ListDevices(ctx)
	if err != nil {
		return err
	}
	for _, dev := range devs {
		if dev.RevokedAt != nil {
			continue
		}
		name := agentstatus.Name(dev)
		picks = append(picks,
			pick{name + " memori", " MB", "agent", dev.ID, decode(func(v agentstats.Stats) float64 { return v.PeakPrivMB })},
			pick{name + " CPU", "%", "agent", dev.ID, decode(func(v agentstats.Stats) float64 { return v.CPUPct })},
		)
	}
	for _, p := range picks {
		samples, err := s.DB.StatsSince(ctx, p.scope, p.dev, since)
		if err != nil {
			return err
		}
		sr := Series{Label: p.label, Unit: p.unit}
		var sum float64
		for _, sm := range samples {
			v, ok := p.f(sm.Data)
			if !ok {
				continue
			}
			sr.N++
			sum += v
			sr.Max = max(sr.Max, v)
		}
		if sr.N > 0 {
			sr.Avg = sum / float64(sr.N)
		}
		d.Last24h = append(d.Last24h, sr)
	}
	return nil
}

// Sample stores the server and host numbers if the last sample is older than
// SampleEvery. Agent samples are stored by the API as heartbeats arrive.
func (s *Service) Sample(ctx context.Context) error {
	now := s.Now()
	last, err := s.DB.LastStatsSampleAt(ctx, "server", 0)
	if err != nil {
		return err
	}
	if !last.IsZero() && now.Sub(last) < SampleEvery {
		return nil
	}
	ts := now.Truncate(time.Second)
	if err := s.DB.AddStatsSample(ctx, ts, "server", 0, s.Server.Snapshot(ctx)); err != nil {
		return err
	}
	if s.HostFile != "" {
		if h, err := sysstats.ReadHost(s.HostFile, now); err == nil {
			return s.DB.AddStatsSample(ctx, ts, "host", 0, h)
		}
	}
	return nil
}

// Summary is the server part of the report, shown in the laptop's bubble.
func (s *Service) Summary(ctx context.Context) (string, error) {
	d, err := s.Gather(ctx)
	if err != nil {
		return "", err
	}
	lines := d.serverLines(false)
	lines = append(lines, d.vpsLines(false)...)
	lines = append(lines, d.llmLines(false)...)
	return Plain(strings.Join(lines, "\n")), nil
}

var plainer = strings.NewReplacer("✅", "√", "⚠️", "!", "🖥 ", "", "🗄 ", "", "🤖 ", "", "💻 ", "", "🔎 ", "", "💾 ", "", "🌱 ", "", "📈 ", "")

// Plain rewrites a report for the overlay bubble, whose font has no emoji.
func Plain(s string) string { return plainer.Replace(s) }

// Button is an inline button.
type Button struct{ Text, Data string }

// Card is a report with its keyboard.
type Card struct {
	Text string
	Rows [][]Button
}

// CallbackPrefix marks the callback data of the report buttons.
const CallbackPrefix = "st:"

func viewKey(v string) string {
	if v == ViewAll {
		return "all"
	}
	return v
}

func keyboard(current string) [][]Button {
	return [][]Button{{
		{"🔄 Segarkan", CallbackPrefix + "r:" + viewKey(current)},
		{"💻 Agen", CallbackPrefix + ViewAgent},
		{"🖥 VPS", CallbackPrefix + ViewVPS},
		{"🤖 LLM", CallbackPrefix + ViewLLM},
		{"📈 24 jam", CallbackPrefix + View24h},
	}}
}

// Report renders a view with its buttons.
func (s *Service) Report(ctx context.Context, view string) (Card, error) {
	d, err := s.Gather(ctx)
	if err != nil {
		return Card{}, err
	}
	if view == View24h {
		if err := s.With24h(ctx, &d); err != nil {
			return Card{}, err
		}
	}
	return Card{Text: d.Render(view), Rows: keyboard(view)}, nil
}

// Callback handles a button tap. ok=false when data is not a report button.
func (s *Service) Callback(ctx context.Context, data string) (Card, bool, error) {
	rest, ok := strings.CutPrefix(data, CallbackPrefix)
	if !ok {
		return Card{}, false, nil
	}
	view := rest
	switch {
	case rest == "r" || rest == "r:all":
		view = ViewAll
	case strings.HasPrefix(rest, "r:"):
		view = strings.TrimPrefix(rest, "r:")
	}
	if !ValidView(view) {
		return Card{}, false, fmt.Errorf("unknown status view %q", view)
	}
	c, err := s.Report(ctx, view)
	return c, true, err
}
