package worklog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// The calendar of the laptop (PRD Fase 8d) reads the same rules as the
// Telegram calendar: what a day is (working day, weekend, holiday, ...) is
// decided here, once, and the laptop only draws it.

// Day kinds sent to the laptop.
const (
	KindWorkday = "workday"
	KindWeekend = "weekend"
	KindHoliday = "holiday"
	KindLeave   = "leave" // cuti bersama: still a working day
	KindOff     = "off"   // the owner marked it off (/libur)
	KindWork    = "work"  // the owner marked it a working day (/kerja)
)

// CalDay is one day of a month.
type CalDay struct {
	D           int    `json:"d"`
	Count       int    `json:"count"`
	Kind        string `json:"kind"`
	HolidayName string `json:"holiday_name,omitempty"`
}

// CalMonth is the month reply.
type CalMonth struct {
	Month    string   `json:"month"` // YYYY-MM
	Today    string   `json:"today"` // YYYY-MM-DD, the owner's local date
	Days     []CalDay `json:"days"`
	Workdays int      `json:"workdays"`
	Filled   int      `json:"filled"`
}

// CalEntry is one entry of a day.
type CalEntry struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// CalDayView is the day reply.
type CalDayView struct {
	Date        string     `json:"date"`
	Kind        string     `json:"kind"`
	HolidayName string     `json:"holiday_name,omitempty"`
	Entries     []CalEntry `json:"entries"`
}

func kindName(r Rules, d time.Time) string {
	switch r.Overrides[key(d)] {
	case "off":
		return KindOff
	case "work":
		return KindWork
	}
	switch r.Kind(d) {
	case Weekend:
		return KindWeekend
	case Holiday:
		return KindHoliday
	case Leave:
		return KindLeave
	}
	return KindWorkday
}

// holidayName is the name to show for a day ("" if none). The owner's own
// marks have no name worth showing.
func holidayName(r Rules, d time.Time) string {
	if _, marked := r.Overrides[key(d)]; marked {
		return ""
	}
	name := ""
	for _, h := range r.Holidays[key(d)] {
		if name == "" || h.Type == "holiday" { // a national holiday wins over cuti bersama
			name = h.Name
		}
	}
	return name
}

// ParseMonth reads "YYYY-MM".
func ParseMonth(s string) (time.Time, error) { return time.Parse("2006-01", s) }

// ParseDate reads "YYYY-MM-DD".
func ParseDate(s string) (time.Time, error) { return time.Parse(time.DateOnly, s) }

// AgentMonth is the month grid data.
func (s *Service) AgentMonth(ctx context.Context, month time.Time) (CalMonth, error) {
	from, to := monthBounds(month)
	counts, err := s.DB.WorklogCounts(ctx, from, to)
	if err != nil {
		return CalMonth{}, err
	}
	rules, err := s.Rules(ctx, from, to)
	if err != nil {
		return CalMonth{}, err
	}
	today := s.today()
	v := BuildMonth(month, today, counts, rules)
	out := CalMonth{Month: from.Format("2006-01"), Today: today.Format(time.DateOnly), Workdays: v.Workdays, Filled: v.Filled}
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		out.Days = append(out.Days, CalDay{D: d.Day(), Count: counts[key(d)], Kind: kindName(rules, d), HolidayName: holidayName(rules, d)})
	}
	return out, nil
}

// AgentDay is the entries of a day.
func (s *Service) AgentDay(ctx context.Context, d time.Time) (CalDayView, error) {
	es, err := s.DB.WorklogByDate(ctx, d)
	if err != nil {
		return CalDayView{}, err
	}
	rules, err := s.Rules(ctx, d, d.AddDate(0, 0, 1))
	if err != nil {
		return CalDayView{}, err
	}
	out := CalDayView{Date: d.Format(time.DateOnly), Kind: kindName(rules, d), HolidayName: holidayName(rules, d), Entries: []CalEntry{}}
	for _, e := range es {
		out.Entries = append(out.Entries, CalEntry{ID: e.ID, Text: e.Text})
	}
	return out, nil
}

// ErrGone means the entry does not exist (any more).
var ErrGone = errors.New("worklog entry not found")

// AgentEdit replaces an entry's text (validated like the Telegram edit).
func (s *Service) AgentEdit(ctx context.Context, id int64, text string) (CalEntry, error) {
	t, verr := ValidateText(text)
	if verr != nil {
		return CalEntry{}, &InputError{Reason: verr.Error()}
	}
	e, err := s.DB.UpdateWorklog(ctx, id, t, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return CalEntry{}, ErrGone
	}
	if err != nil {
		return CalEntry{}, fmt.Errorf("edit entry: %w", err)
	}
	return CalEntry{ID: e.ID, Text: e.Text}, nil
}

// AgentDelete removes an entry. Deleting one that is already gone is not an
// error for the caller (the laptop may be resending).
func (s *Service) AgentDelete(ctx context.Context, id int64) error {
	_, err := s.DB.DeleteWorklog(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrGone
	}
	return err
}
