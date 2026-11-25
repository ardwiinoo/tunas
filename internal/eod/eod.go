// Package eod builds the end-of-day report (PRD Fase 8f): deterministic text
// (no LLM) with the day's active time, time per project, top apps, the
// longest focus session, the worklog status and suggestions for projects that
// the worklog does not mention yet.
package eod

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/projects"
	"github.com/ardwiinoo/tunas/internal/projreport"
	"github.com/ardwiinoo/tunas/internal/reminders"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Settings keys (PRD 5.3).
const (
	KeyEnabled = "report.eod_enabled" // bool, default true
	KeyTime    = "report.eod_time"    // "HH:MM", default 17:45
	KeySent    = "report.eod_sent"    // YYYY-MM-DD of the last report
	KeySuggest = "report.eod_suggest" // the suggestions behind the buttons
)

// Tuning.
const (
	DefaultTime    = "17:45"
	MinProject     = 30 * time.Minute // a project this long should be in the worklog
	LateWindow     = 3 * time.Hour    // a report is not sent later than this after its time
	TickEvery      = 30 * time.Second
	callbackPrefix = "eod:"
)

// Button and Card are a message with inline buttons.
type Button struct{ Text, Data string }

// Card is a message with an optional keyboard.
type Card struct {
	Text  string
	Rows  [][]Button
	Toast string
}

// Worklog is what the report needs from the worklog.
type Worklog interface {
	// Workday reports whether the day is a working day (holidays, leave, weekends).
	Workday(ctx context.Context, d time.Time) (bool, error)
	// Add saves an entry for a day (and lets the GitHub backup know).
	Add(ctx context.Context, d time.Time, text string) error
}

// Suggestion is a project that is missing from the worklog.
type Suggestion struct {
	Project string `json:"project"`
	Text    string `json:"text"` // "Coding: project-A 1j50m"
	Added   bool   `json:"added,omitempty"`
}

// Input is the data of one day.
type Input struct {
	Day          time.Time
	Loc          *time.Location
	Summary      activity.Summary
	Projects     []store.ProjectTotal
	WorklogTexts []string
}

// Report is the text and the suggestions behind its buttons.
type Report struct {
	Text        string
	Suggestions []Suggestion
}

// Missing returns the projects of at least MinProject whose name (or an
// original name behind an alias) does not occur in any worklog text, ignoring
// letter case.
func Missing(ts []store.ProjectTotal, texts []string) []Suggestion {
	hay := strings.ToLower(strings.Join(texts, "\n"))
	var out []Suggestion
	for _, t := range ts {
		d := time.Duration(t.Seconds) * time.Second
		if d < MinProject || t.Name == projects.NoProject {
			continue
		}
		names := append([]string{t.Name}, t.Originals...)
		found := false
		for _, n := range names {
			if n != "" && strings.Contains(hay, strings.ToLower(n)) {
				found = true
			}
		}
		if !found {
			out = append(out, Suggestion{Project: t.Name, Text: fmt.Sprintf("Coding: %s %s", t.Name, projreport.Compact(d))})
		}
	}
	return out
}

// Build renders the report.
func Build(in Input) Report {
	var b strings.Builder
	fmt.Fprintf(&b, "📋 Laporan akhir hari — %s", reminders.FormatDate(in.Day, in.Day, in.Loc))
	s := in.Summary
	if s.TotalActive == 0 {
		b.WriteString("\nBelum ada data aktivitas hari ini.")
	} else {
		fmt.Fprintf(&b, "\nTotal waktu aktif: %s", activity.FormatDuration(s.TotalActive))
	}
	if sec := projreport.Section(in.Projects, 8); sec != "" {
		b.WriteString("\n\n" + sec)
	}
	if len(s.Apps) > 0 {
		var apps []string
		for i, a := range s.Apps {
			if i == 3 {
				break
			}
			apps = append(apps, fmt.Sprintf("%s %s", a.App, activity.FormatDuration(a.Active)))
		}
		b.WriteString("\n\nAplikasi teratas: " + strings.Join(apps, " · "))
	}
	if l := s.Longest; l != nil {
		fmt.Fprintf(&b, "\nSesi fokus terpanjang: %s %s (%s–%s)", l.App, activity.FormatDuration(l.Duration()),
			l.Start.In(in.Loc).Format("15:04"), l.End.In(in.Loc).Format("15:04"))
	}
	if n := len(in.WorklogTexts); n == 0 {
		b.WriteString("\n\nWorklog hari ini: belum ada catatan.")
	} else {
		fmt.Fprintf(&b, "\n\nWorklog hari ini: %d catatan.", n)
	}
	sug := Missing(in.Projects, in.WorklogTexts)
	if len(sug) > 0 {
		b.WriteString("\nBelum disebut di worklog:")
		for _, g := range sug {
			b.WriteString("\n• " + g.Text)
		}
	}
	return Report{Text: b.String(), Suggestions: sug}
}

// Service sends the report and answers its buttons and command.
type Service struct {
	DB      *store.DB
	Now     func() time.Time
	Loc     *time.Location
	Log     *slog.Logger
	Worklog Worklog
	Send    func(ctx context.Context, c Card) error // nil = report cannot be sent
}

// Settings are the report settings.
type Settings struct {
	Enabled bool
	Time    string
}

// Load reads the settings with defaults.
func (s *Service) Load(ctx context.Context) (Settings, error) {
	st := Settings{Enabled: true, Time: DefaultTime}
	if _, err := s.DB.GetSetting(ctx, KeyEnabled, &st.Enabled); err != nil {
		return st, err
	}
	if _, err := s.DB.GetSetting(ctx, KeyTime, &st.Time); err != nil {
		return st, err
	}
	return st, nil
}

// ParseClock parses "HH:MM" (also "H.MM").
func ParseClock(s string) (h, m int, ok bool) {
	s = strings.Replace(strings.TrimSpace(s), ".", ":", 1)
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// Gather loads the data of a day.
func (s *Service) Gather(ctx context.Context, day time.Time) (Input, error) {
	from, to := activity.DayRange(day, s.Loc)
	bs, err := s.DB.BucketsBetween(ctx, from, to)
	if err != nil {
		return Input{}, err
	}
	ps, err := s.DB.ProjectTotalsBetween(ctx, from, to)
	if err != nil {
		return Input{}, err
	}
	es, err := s.DB.WorklogByDate(ctx, time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC))
	if err != nil {
		return Input{}, err
	}
	in := Input{Day: day, Loc: s.Loc, Summary: activity.Summarize(bs), Projects: ps}
	for _, e := range es {
		in.WorklogTexts = append(in.WorklogTexts, e.Text)
	}
	return in, nil
}

type suggestState struct {
	Day   string       `json:"day"`
	Items []Suggestion `json:"items"`
}

func card(r Report) Card {
	c := Card{Text: r.Text}
	for i, g := range r.Suggestions {
		label := "➕ Tambah ke worklog: " + g.Project
		if r := []rune(label); len(r) > 40 {
			label = string(r[:39]) + "…"
		}
		c.Rows = append(c.Rows, []Button{{Text: label, Data: fmt.Sprintf("%sa:%d", callbackPrefix, i)}})
	}
	return c
}

// Report builds today's report and remembers its suggestions for the buttons.
func (s *Service) Report(ctx context.Context) (Card, error) {
	day := s.Now().In(s.Loc)
	in, err := s.Gather(ctx, day)
	if err != nil {
		return Card{}, err
	}
	r := Build(in)
	if err := s.DB.SetSetting(ctx, KeySuggest, suggestState{Day: day.Format("2006-01-02"), Items: r.Suggestions}); err != nil {
		return Card{}, err
	}
	return card(r), nil
}

// Tick sends the report once a working day, from the configured time until
// LateWindow later.
func (s *Service) Tick(ctx context.Context) error {
	if s.Send == nil {
		return nil
	}
	st, err := s.Load(ctx)
	if err != nil || !st.Enabled {
		return err
	}
	h, m, ok := ParseClock(st.Time)
	if !ok {
		return nil
	}
	now := s.Now().In(s.Loc)
	due := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, s.Loc)
	if now.Before(due) || now.Sub(due) >= LateWindow {
		return nil
	}
	today := now.Format("2006-01-02")
	var sent string
	if _, err := s.DB.GetSetting(ctx, KeySent, &sent); err != nil || sent == today {
		return err
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	work, err := s.Worklog.Workday(ctx, day)
	if err != nil {
		return err
	}
	if !work {
		return s.DB.SetSetting(ctx, KeySent, today) // nothing to report today; do not look again
	}
	c, err := s.Report(ctx)
	if err != nil {
		return err
	}
	if err := s.Send(ctx, c); err != nil {
		return err // tried again on the next tick
	}
	return s.DB.SetSetting(ctx, KeySent, today)
}

// Run ticks until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(TickEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil && s.Log != nil {
			s.Log.Warn("end-of-day report failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Command handles /laporan [on|off|jam HH:MM|sekarang].
func (s *Service) Command(ctx context.Context, args string) (Card, error) {
	f := strings.Fields(strings.ToLower(args))
	st, err := s.Load(ctx)
	if err != nil {
		return Card{}, err
	}
	switch {
	case len(f) == 0:
		on := "mati"
		if st.Enabled {
			on = "nyala"
		}
		return Card{Text: fmt.Sprintf("Laporan akhir hari: %s, jam %s (hanya hari kerja).\n/laporan on|off · /laporan jam HH:MM · /laporan sekarang", on, st.Time)}, nil
	case f[0] == "on" || f[0] == "off":
		if err := s.DB.SetSetting(ctx, KeyEnabled, f[0] == "on"); err != nil {
			return Card{}, err
		}
		return Card{Text: "✅ Laporan akhir hari sekarang " + f[0] + "."}, nil
	case f[0] == "jam" && len(f) == 2:
		h, m, ok := ParseClock(f[1])
		if !ok {
			return Card{Text: "Pakai: /laporan jam HH:MM, mis. /laporan jam 17:45"}, nil
		}
		clock := fmt.Sprintf("%02d:%02d", h, m)
		if err := s.DB.SetSetting(ctx, KeyTime, clock); err != nil {
			return Card{}, err
		}
		// a new time applies today too
		_ = s.DB.DeleteSetting(ctx, KeySent)
		return Card{Text: "✅ Laporan akhir hari dikirim jam " + clock + " pada hari kerja."}, nil
	case f[0] == "sekarang":
		return s.Report(ctx)
	}
	return Card{Text: "Pakai: /laporan [on|off|jam HH:MM|sekarang]"}, nil
}

// Callback handles "➕ Tambah ke worklog". ok=false for other buttons.
func (s *Service) Callback(ctx context.Context, data string) (Card, bool, error) {
	rest, ok := strings.CutPrefix(data, callbackPrefix+"a:")
	if !ok {
		return Card{}, false, nil
	}
	var idx int
	if _, err := fmt.Sscanf(rest, "%d", &idx); err != nil {
		return Card{Toast: "Tombol tidak dikenal."}, true, nil
	}
	var st suggestState
	if found, err := s.DB.GetSetting(ctx, KeySuggest, &st); err != nil {
		return Card{}, true, err
	} else if !found || idx < 0 || idx >= len(st.Items) {
		return Card{Toast: "Saran ini sudah kedaluwarsa."}, true, nil
	}
	g := st.Items[idx]
	day, err := time.Parse("2006-01-02", st.Day)
	if err != nil {
		return Card{Toast: "Saran ini sudah kedaluwarsa."}, true, nil
	}
	if g.Added {
		return Card{Toast: "Sudah ditambahkan."}, true, nil
	}
	if err := s.Worklog.Add(ctx, day, g.Text); err != nil {
		return Card{}, true, err
	}
	st.Items[idx].Added = true
	if err := s.DB.SetSetting(ctx, KeySuggest, st); err != nil {
		return Card{}, true, err
	}
	return Card{Text: "✅ Ditambahkan ke worklog: " + g.Text + "\nUntuk menyunting, buka /cal dan ketuk tanggalnya.", Toast: "Ditambahkan"}, true, nil
}
