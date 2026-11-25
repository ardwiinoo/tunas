package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func calAt(y int, m time.Month, d int) *Calendar { return NewCalendar(date(y, m, d)) }

// The grid starts on Monday and has 5 or 6 rows; every cell is inside the
// card and none overlap.
func TestCalendarLayout(t *testing.T) {
	cases := []struct {
		name       string
		y          int
		m          time.Month
		firstCol   int
		rows, days int
	}{
		{"February 2026 (28 days, starts Sunday... col 6)", 2026, time.February, 6, 5, 28},
		{"February 2027 (28 days, starts Monday: 4 weeks + ...)", 2027, time.February, 0, 4, 28},
		{"November 2026 (30 days, starts Sunday: 6 rows)", 2026, time.November, 6, 6, 30},
		{"August 2026 (31 days, starts Saturday: 6 rows)", 2026, time.August, 5, 6, 31},
		{"March 2026 (31 days, starts Sunday: 6 rows)", 2026, time.March, 6, 6, 31},
		{"June 2026 (30 days, starts Monday)", 2026, time.June, 0, 5, 30},
	}
	for _, c := range cases {
		cal := calAt(c.y, c.m, 1)
		l := cal.Layout()
		if cal.DaysInMonth() != c.days || l.FirstCol != c.firstCol || l.Rows != c.rows || len(l.Cells) != c.days {
			t.Errorf("%s: days %d col %d rows %d cells %d", c.name, cal.DaysInMonth(), l.FirstCol, l.Rows, len(l.Cells))
		}
		seen := map[Rect]int{}
		for d, r := range l.Cells {
			if r.X < l.Card.X || r.X+r.W > l.Card.X+l.Card.W+0.001 || r.Y < l.Card.Y || r.Y+r.H > l.Footer.Y {
				t.Errorf("%s: day %d at %+v leaves the card or reaches the footer %+v", c.name, d, r, l.Footer)
			}
			if prev, dup := seen[r]; dup {
				t.Errorf("%s: days %d and %d share a cell", c.name, prev, d)
			}
			seen[r] = d
		}
		// the 1st sits in its weekday's column, and the 8th right below it
		if x1, x8 := l.Cells[1].X, l.Cells[8].X; x1 != x8 || l.Cells[8].Y <= l.Cells[1].Y {
			t.Errorf("%s: 1st %+v, 8th %+v", c.name, l.Cells[1], l.Cells[8])
		}
		if len(l.Head) != 7 || l.Head[0].X != l.Cells[1].X-float64(c.firstCol)*l.CellW {
			t.Errorf("%s: header columns do not line up with the grid", c.name)
		}
	}
	// a 6-row month still fits above the footer
	l := calAt(2026, time.March, 1).Layout()
	last := l.Cells[31]
	if last.Y+last.H > l.Footer.Y {
		t.Errorf("last row %+v runs into the footer %+v", last, l.Footer)
	}
	if got := calAt(2026, time.November, 1).Title(); got != "November 2026" {
		t.Errorf("title %q", got)
	}
}

func TestMonthNavigation(t *testing.T) {
	c := calAt(2026, time.November, 18)
	if c.Sel != 18 || c.MonthKey() != "2026-11" {
		t.Fatalf("starts on today: %d %s", c.Sel, c.MonthKey())
	}
	if r := c.Key(KeyRight); r.Kind != CalNone || c.Sel != 19 {
		t.Fatalf("right: %+v sel %d", r, c.Sel)
	}
	if r := c.Key(KeyDown); r.Kind != CalNone || c.Sel != 26 {
		t.Fatalf("down: sel %d", c.Sel)
	}
	if r := c.Key(KeyUp); c.Sel != 19 || r.Kind != CalNone {
		t.Fatalf("up: sel %d", c.Sel)
	}
	// crossing the month edge loads the neighbour
	c.Sel = 30
	if r := c.Key(KeyRight); r.Kind != CalLoadMonth || r.Month != "2026-12" || c.Sel != 1 || c.MonthKey() != "2026-12" {
		t.Fatalf("right at the end of the month: %+v %s %d", r, c.MonthKey(), c.Sel)
	}
	if r := c.Key(KeyLeft); r.Kind != CalLoadMonth || r.Month != "2026-11" || c.Sel != 30 {
		t.Fatalf("left at the start: %+v %s %d", r, c.MonthKey(), c.Sel)
	}
	// down from the 26th: into December at day 3
	c.Sel = 26
	if r := c.Key(KeyDown); r.Month != "2026-12" || c.Sel != 3 {
		t.Fatalf("down across: %+v %d", r, c.Sel)
	}
	// PageUp/PageDown change the month and clamp the day
	c = calAt(2026, time.January, 31)
	if r := c.Key(KeyPageDown); r.Kind != CalLoadMonth || r.Month != "2026-02" || c.Sel != 28 {
		t.Fatalf("pagedown from 31 Jan: %+v sel %d", r, c.Sel)
	}
	if r := c.Key(KeyPageUp); r.Month != "2026-01" {
		t.Fatalf("pageup: %+v", r)
	}
	c = calAt(2026, time.January, 5)
	if r := c.Key(KeyPageUp); r.Month != "2025-12" || c.Month.Year() != 2025 {
		t.Fatalf("year change: %+v", r)
	}
	// T goes back to today, and does nothing when already there
	c = calAt(2026, time.November, 18)
	c.Key(KeyPageDown)
	if r := c.Key(KeyToday); r.Month != "2026-11" || c.Sel != 18 {
		t.Fatalf("T: %+v %d", r, c.Sel)
	}
	if r := c.Key(KeyToday); r.Kind != CalNone {
		t.Fatalf("T on today: %+v", r)
	}
	if r := c.Type([]rune("t")); r.Kind != CalNone || c.Sel != 18 {
		t.Fatalf("typed t on today: %+v", r)
	}
	if r := c.Key(KeyEsc); r.Kind != CalClose {
		t.Fatalf("esc closes the grid: %+v", r)
	}
}

func TestMarksAndSummary(t *testing.T) {
	c := calAt(2026, time.November, 18)
	if c.Summary() != "memuat..." {
		t.Errorf("before load: %q", c.Summary())
	}
	days := make([]agent.CalDay, 30)
	for i := range days {
		days[i] = agent.CalDay{D: i + 1, Kind: "workday"}
	}
	for _, d := range []int{1, 7, 8, 14, 15, 21, 22, 28, 29} {
		days[d-1].Kind = "weekend"
	}
	days[1].Count = 2                                           // 2 Nov: filled
	days[2].Kind = "holiday"                                    // 3 Nov: a holiday
	days[3].Kind, days[3].HolidayName = "leave", "Cuti Bersama" // 4 Nov
	days[4].Kind = "off"                                        // 5 Nov: marked off
	days[5].Kind = "work"                                       // 6 Nov: marked working
	days[17].Count = 0                                          // 18 Nov: today, empty
	c.SetMonth(agent.CalMonth{Month: "2026-11", Today: "2026-11-18", Days: days, Workdays: 12, Filled: 10}, false)
	for d, want := range map[int]DayMark{
		2: MarkFilled, 3: MarkHoliday, 4: MarkLeave, 5: MarkHoliday, 6: MarkMissed, 9: MarkMissed,
		7: MarkWeekend, 18: MarkNone, 19: MarkNone, 30: MarkNone,
	} {
		if got := c.MarkOf(d); got != want {
			t.Errorf("day %d mark %v, want %v", d, got, want)
		}
	}
	if got := c.Summary(); got != "10/12 hari kerja terisi" {
		t.Errorf("summary %q", got)
	}
	c.SetMonth(agent.CalMonth{Month: "2026-11", Days: days, Workdays: 12, Filled: 10}, true)
	if got := c.Summary(); got != "10/12 hari kerja terisi (data lama)" || c.Note != "data lama (offline)" {
		t.Errorf("stale: %q note %q", got, c.Note)
	}
	c.SetMonth(agent.CalMonth{Month: "2026-11", Days: days, Workdays: 12, Filled: 10}, false)
	if c.Note != "" {
		t.Errorf("fresh data clears the stale note: %q", c.Note)
	}
	// data for another month is ignored (a late answer after navigating on)
	c.SetMonth(agent.CalMonth{Month: "2026-10", Workdays: 99}, false)
	if c.Data.Workdays != 12 {
		t.Error("a late reply for another month replaced the shown one")
	}
	c.Sel = 4
	if c.SelectedName() != "Cuti Bersama" {
		t.Errorf("name %q", c.SelectedName())
	}
}

func dayPanel(t *testing.T, entries ...string) *Calendar {
	t.Helper()
	c := calAt(2026, time.November, 17)
	if r := c.Key(KeyEnter); r.Kind != CalLoadDay || r.Date != "2026-11-17" || c.Mode != CalDayPanel || !c.Busy {
		t.Fatalf("enter: %+v", r)
	}
	v := agent.CalDayView{Date: "2026-11-17", Kind: "workday"}
	for i, e := range entries {
		v.Entries = append(v.Entries, agent.CalEntry{ID: int64(10 + i), Text: e})
	}
	c.SetDay(v, false)
	return c
}

func TestDayPanelAddEditDelete(t *testing.T) {
	c := dayPanel(t, "rapat", "review PR")
	if c.Busy || c.Focus != FocusInput {
		t.Fatalf("loaded: busy %v focus %v", c.Busy, c.Focus)
	}

	// typing then Enter adds an entry for the selected day
	c.Type([]rune("deploy staging"))
	if r := c.Key(KeyEnter); r.Kind != CalAdd || r.Date != "2026-11-17" || r.Text != "deploy staging" {
		t.Fatalf("add: %+v", r)
	}
	c.Done(CalAdd, nil, false)
	if c.Draft != "" || c.Note != "Tersimpan" {
		t.Errorf("after add: %q %q", c.Draft, c.Note)
	}
	if r := c.ReloadDay(); r.Kind != CalLoadDay || r.Date != "2026-11-17" {
		t.Errorf("reload: %+v", r)
	}
	c.Done(CalAdd, nil, true)
	if !strings.Contains(c.Note, "dikirim saat online") {
		t.Errorf("queued note %q", c.Note)
	}
	// an empty draft sends nothing
	if r := c.Key(KeyEnter); r.Kind != CalNone {
		t.Errorf("empty enter: %+v", r)
	}

	// Up goes to the list, Up again to the first entry; E edits the selected one
	c.Key(KeyUp)
	if c.Focus != FocusList || c.EntrySel != 1 {
		t.Fatalf("up: focus %v sel %d", c.Focus, c.EntrySel)
	}
	c.Key(KeyUp)
	if c.EntrySel != 0 {
		t.Fatalf("sel %d", c.EntrySel)
	}
	c.Type([]rune("e")) // E in the list edits, it is not typed
	if c.Editing != 10 || c.Draft != "rapat" || c.Focus != FocusInput {
		t.Fatalf("edit: editing %d draft %q focus %v", c.Editing, c.Draft, c.Focus)
	}
	c.Type([]rune(" sprint"))
	if r := c.Key(KeyEnter); r.Kind != CalEdit || r.ID != 10 || r.Text != "rapat sprint" {
		t.Fatalf("save edit: %+v", r)
	}
	c.Done(CalEdit, nil, false)
	if c.Editing != 0 || c.Draft != "" || c.Note != "Diubah" {
		t.Errorf("after edit: %d %q %q", c.Editing, c.Draft, c.Note)
	}

	// Del asks first; Enter confirms, anything else cancels
	c.Key(KeyUp)
	c.Key(KeyDelete)
	if c.ConfirmID != 11 {
		t.Fatalf("confirm id %d", c.ConfirmID)
	}
	if r := c.Key(KeyEsc); r.Kind != CalNone || c.ConfirmID != 0 || c.Mode != CalDayPanel {
		t.Fatalf("cancel: %+v confirm %d mode %v", r, c.ConfirmID, c.Mode)
	}
	c.Key(KeyDelete)
	if r := c.Key(KeyEnter); r.Kind != CalDelete || r.ID != 11 {
		t.Fatalf("confirmed delete: %+v", r)
	}
	c.Done(CalDelete, nil, false)
	if c.Note != "Dihapus" {
		t.Errorf("note %q", c.Note)
	}
	// typing while a confirmation is open does nothing
	c.Key(KeyDelete)
	c.Type([]rune("abc"))
	if c.Draft != "" {
		t.Errorf("typed during the confirmation: %q", c.Draft)
	}
	c.Key(KeyEsc)
}

func TestDayPanelEscapeAndFocus(t *testing.T) {
	c := dayPanel(t, "satu")
	// typing in the list starts a new entry instead
	c.Key(KeyUp)
	c.Type([]rune("x"))
	if c.Focus != FocusInput || c.Draft != "x" {
		t.Fatalf("typed in the list: focus %v draft %q", c.Focus, c.Draft)
	}
	// Up with text in the input keeps the focus (it would lose the draft's place)
	c.Key(KeyUp)
	if c.Focus != FocusInput {
		t.Errorf("focus left a non-empty input")
	}
	// first Esc clears the input, the second goes back to the grid and reloads the counts
	if r := c.Key(KeyEsc); r.Kind != CalNone || c.Draft != "" || c.Mode != CalDayPanel {
		t.Fatalf("esc 1: %+v draft %q", r, c.Draft)
	}
	if r := c.Key(KeyEsc); r.Kind != CalLoadMonth || c.Mode != CalGrid || r.Month != "2026-11" {
		t.Fatalf("esc 2: %+v mode %v", r, c.Mode)
	}
	// E and Del outside the list are not commands
	c = dayPanel(t, "satu")
	c.Type([]rune("e"))
	if c.Draft != "e" || c.Editing != 0 {
		t.Errorf("E in the input is a letter: %q", c.Draft)
	}
	c.Key(KeyDelete)
	if c.ConfirmID != 0 {
		t.Error("Del outside the list")
	}
	// backspace removes whole characters
	c.Draft = "abé"
	c.Key(KeyBackspace)
	if c.Draft != "ab" {
		t.Errorf("backspace: %q", c.Draft)
	}
	// a late day reply for another date is ignored
	c.SetDay(agent.CalDayView{Date: "2026-11-30", Entries: []agent.CalEntry{{ID: 1, Text: "x"}}}, false)
	if len(c.Day.Entries) != 1 || c.Day.Entries[0].Text != "satu" {
		t.Error("a reply for another day replaced the panel")
	}
}

func TestDayPanelPaste(t *testing.T) {
	c := dayPanel(t)
	c.Paste("- satu\r\n- dua\r\n- tiga")
	if c.Draft != "- satu\n- dua\n- tiga" {
		t.Fatalf("draft %q", c.Draft)
	}
	if r := c.Key(KeyEnter); r.Kind != CalAdd || r.Text != "- satu\n- dua\n- tiga" {
		t.Fatalf("add: %+v (the server splits the lines like /log)", r)
	}
	c.Done(CalAdd, nil, false)
	// bounded
	c.Paste(strings.Repeat("x", MaxCalDraft+500))
	if n := len([]rune(c.Draft)); n != MaxCalDraft {
		t.Fatalf("draft is %d runes", n)
	}
	c.Type([]rune("more"))
	if n := len([]rune(c.Draft)); n != MaxCalDraft {
		t.Fatalf("typing past the limit: %d", n)
	}
	// control characters are not typed
	c.Draft = ""
	c.Type([]rune{'a', 0x07, 'b', 0x1b})
	if c.Draft != "ab" {
		t.Errorf("draft %q", c.Draft)
	}
	// pasting in the grid does nothing
	g := calAt(2026, time.November, 1)
	g.Paste("x")
	if g.Draft != "" {
		t.Error("paste into the grid")
	}
}

func TestFailuresAreShown(t *testing.T) {
	c := dayPanel(t, "a")
	c.Type([]rune("b"))
	c.Key(KeyEnter)
	c.Done(CalAdd, errTest("server tidak terjangkau"), false)
	if c.Busy || c.Draft != "b" || !strings.HasPrefix(c.Note, "Gagal: server") {
		t.Fatalf("a failed add keeps the draft: busy %v draft %q note %q", c.Busy, c.Draft, c.Note)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
