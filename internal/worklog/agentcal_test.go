package worklog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestAgentMonthMatchesTheTelegramCalendar(t *testing.T) {
	s, db, _ := newService(t) // "now" is Wed 18 Nov 2026
	ctx := context.Background()
	add := func(d time.Time, text string) {
		if _, _, err := db.AddWorklog(ctx, d, text, "telegram"); err != nil {
			t.Fatal(err)
		}
	}
	add(day(2026, 11, 2), "rapat")
	add(day(2026, 11, 2), "review")
	add(day(2026, 11, 3), "deploy")
	if err := db.UpsertHolidays(ctx, []store.Holiday{
		{Day: day(2026, 11, 10), Name: "Hari Pahlawan", Type: "holiday"},
		{Day: day(2026, 11, 12), Name: "Cuti Bersama", Type: "leave"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetOverride(ctx, day(2026, 11, 14), "work", ""); err != nil { // a Saturday marked as a working day
		t.Fatal(err)
	}
	if err := db.SetOverride(ctx, day(2026, 11, 16), "off", ""); err != nil { // a Monday marked off
		t.Fatal(err)
	}

	m, err := s.AgentMonth(ctx, day(2026, 11, 20))
	if err != nil {
		t.Fatal(err)
	}
	if m.Month != "2026-11" || m.Today != "2026-11-18" || len(m.Days) != 30 {
		t.Fatalf("month %+v", m)
	}
	d := func(n int) CalDay { return m.Days[n-1] }
	for n, want := range map[int]CalDay{
		1:  {D: 1, Kind: KindWorkday}, // Sunday? 1 Nov 2026 is a Sunday
		2:  {D: 2, Count: 2, Kind: KindWorkday},
		3:  {D: 3, Count: 1, Kind: KindWorkday},
		7:  {D: 7, Kind: KindWeekend},
		10: {D: 10, Kind: KindHoliday, HolidayName: "Hari Pahlawan"},
		12: {D: 12, Kind: KindLeave, HolidayName: "Cuti Bersama"},
		14: {D: 14, Kind: KindWork},
		16: {D: 16, Kind: KindOff},
	} {
		got := d(n)
		if n == 1 {
			want.Kind = KindWeekend
		}
		if got != want {
			t.Errorf("day %d = %+v, want %+v", n, got, want)
		}
	}
	// the same numbers as the text calendar: working days before today, filled ones
	v := BuildMonth(day(2026, 11, 20), day(2026, 11, 18), map[string]int{"2026-11-02": 2, "2026-11-03": 1}, mustRules(t, s, ctx))
	if m.Workdays != v.Workdays || m.Filled != v.Filled || m.Filled != 2 {
		t.Fatalf("workdays %d filled %d, telegram says %d/%d", m.Workdays, m.Filled, v.Workdays, v.Filled)
	}
}

func mustRules(t *testing.T, s *Service, ctx context.Context) Rules {
	t.Helper()
	r, err := s.Rules(ctx, day(2026, 11, 1), day(2026, 12, 1))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAgentDayEditDelete(t *testing.T) {
	s, db, _ := newService(t)
	ctx := context.Background()
	e1, _, _ := db.AddWorklog(ctx, day(2026, 11, 17), "satu", "telegram")
	e2, _, _ := db.AddWorklog(ctx, day(2026, 11, 17), "dua", "telegram")

	v, err := s.AgentDay(ctx, day(2026, 11, 17))
	if err != nil || v.Date != "2026-11-17" || v.Kind != KindWorkday || len(v.Entries) != 2 || v.Entries[0].Text != "satu" {
		t.Fatalf("day %+v %v", v, err)
	}
	empty, _ := s.AgentDay(ctx, day(2026, 11, 18))
	if empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatalf("an empty day has an empty list, not null: %+v", empty.Entries)
	}

	got, err := s.AgentEdit(ctx, e1.ID, "  satu\nbaris  baru  ")
	if err != nil || got.Text != "satu baris baru" {
		t.Fatalf("edit %+v %v", got, err)
	}
	var bad *InputError
	if _, err := s.AgentEdit(ctx, e1.ID, "   "); !errors.As(err, &bad) {
		t.Fatalf("empty text: %v", err)
	}
	if _, err := s.AgentEdit(ctx, e1.ID, strings.Repeat("x", 3000)); !errors.As(err, &bad) {
		t.Fatalf("long text: %v", err)
	}
	if _, err := s.AgentEdit(ctx, 99999, "x"); !errors.Is(err, ErrGone) {
		t.Fatalf("missing: %v", err)
	}

	if err := s.AgentDelete(ctx, e2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentDelete(ctx, e2.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("second delete: %v", err)
	}
	if v, _ := s.AgentDay(ctx, day(2026, 11, 17)); len(v.Entries) != 1 || v.Entries[0].Text != "satu baris baru" {
		t.Fatalf("after: %+v", v.Entries)
	}
	// both marked the month for the GitHub backup
	if st, err := db.SyncState(ctx, "2026-11"); err != nil || !st.Dirty {
		t.Fatalf("sync state %+v %v", st, err)
	}
}
