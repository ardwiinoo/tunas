package overlay

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// The worklog calendar card (PRD Fase 8d), model and layout. Everything here
// is pure so it can be tested without a window: the Win32 side feeds it keys
// and carries out the requests it returns.

// Card geometry (logical pixels, inside the 340x300 window; the card covers
// the character while it is open).
const (
	CalCardX, CalCardY = 6, 6
	CalCardW, CalCardH = WindowW - 12, WindowH - 12
	calPad             = 12
	calTitleH          = 24
	calHeadH           = 18
	calCellH           = 28
	calFooterH         = 40
	// MaxCalDraft bounds what can be typed or pasted in the day panel.
	MaxCalDraft = MaxPasteLen
)

// CalMode is month grid or day panel.
type CalMode int

// The two views of the card.
const (
	CalGrid CalMode = iota
	CalDayPanel
)

// CalFocus is where typing goes in the day panel.
type CalFocus int

// Day panel focus.
const (
	FocusInput CalFocus = iota
	FocusList
)

// CalKey is a navigation key.
type CalKey int

// Keys the card understands.
const (
	KeyLeft CalKey = iota
	KeyRight
	KeyUp
	KeyDown
	KeyPageUp
	KeyPageDown
	KeyToday // T
	KeyEnter
	KeyEsc
	KeyEdit // E, in the list
	KeyDelete
	KeyBackspace
)

// CalReqKind is what the card asks the outside world to do.
type CalReqKind int

// Requests returned by the card.
const (
	CalNone CalReqKind = iota
	CalLoadMonth
	CalLoadDay
	CalAdd
	CalEdit
	CalDelete
	CalClose
)

// CalReq is one request (zero value = nothing to do).
type CalReq struct {
	Kind  CalReqKind
	Month string // CalLoadMonth: "YYYY-MM"
	Date  string // CalLoadDay, CalAdd: "YYYY-MM-DD"
	ID    int64  // CalEdit, CalDelete
	Text  string // CalAdd, CalEdit
}

// Calendar is the card's state.
type Calendar struct {
	Today time.Time // local date, midnight UTC
	Month time.Time // first of the shown month
	Sel   int       // selected day of Month
	Mode  CalMode

	Data  *agent.CalMonth // nil until loaded
	Stale bool            // Data comes from the cache
	Busy  bool            // a request is in flight
	Note  string          // short message under the card ("Tersimpan", "Server tidak terjangkau")

	// day panel
	Day       *agent.CalDayView
	DayStale  bool
	Focus     CalFocus
	EntrySel  int
	Draft     string
	Editing   int64 // entry being edited (the draft holds its new text)
	ConfirmID int64 // entry waiting for the delete confirmation
}

// NewCalendar opens the card on today's month.
func NewCalendar(today time.Time) *Calendar {
	t := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	return &Calendar{Today: t, Month: time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), Sel: t.Day()}
}

// MonthKey is "YYYY-MM" of the shown month.
func (c *Calendar) MonthKey() string { return c.Month.Format("2006-01") }

// SelDate is the selected day.
func (c *Calendar) SelDate() time.Time {
	return time.Date(c.Month.Year(), c.Month.Month(), c.Sel, 0, 0, 0, 0, time.UTC)
}

// DaysInMonth is the length of the shown month.
func (c *Calendar) DaysInMonth() int { return c.Month.AddDate(0, 1, -1).Day() }

// SetMonth stores a loaded month (ignored when it is not the shown one).
func (c *Calendar) SetMonth(m agent.CalMonth, stale bool) {
	if m.Month != c.MonthKey() {
		return
	}
	c.Data, c.Stale, c.Busy = &m, stale, false
	if stale {
		c.Note = "data lama (offline)"
	} else if c.Note == "data lama (offline)" {
		c.Note = ""
	}
}

// SetDay stores a loaded day (ignored when it is not the selected one).
func (c *Calendar) SetDay(d agent.CalDayView, stale bool) {
	if d.Date != c.SelDate().Format(time.DateOnly) {
		return
	}
	c.Day, c.DayStale, c.Busy = &d, stale, false
	c.EntrySel = min(c.EntrySel, max(len(d.Entries)-1, 0))
}

func (c *Calendar) shiftMonth(delta int) CalReq {
	first := c.Month.AddDate(0, delta, 0)
	last := first.AddDate(0, 1, -1).Day()
	c.Month, c.Sel = first, min(c.Sel, last)
	c.Data, c.Stale = nil, false
	return CalReq{Kind: CalLoadMonth, Month: c.MonthKey()}
}

// move changes the selection by n days, crossing months when needed.
func (c *Calendar) move(n int) CalReq {
	d := c.SelDate().AddDate(0, 0, n)
	if d.Month() == c.Month.Month() && d.Year() == c.Month.Year() {
		c.Sel = d.Day()
		return CalReq{}
	}
	c.Month, c.Sel = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC), d.Day()
	c.Data, c.Stale = nil, false
	return CalReq{Kind: CalLoadMonth, Month: c.MonthKey()}
}

// Key handles a navigation key and returns what to do next.
func (c *Calendar) Key(k CalKey) CalReq {
	if c.Mode == CalDayPanel {
		return c.dayKey(k)
	}
	switch k {
	case KeyLeft:
		return c.move(-1)
	case KeyRight:
		return c.move(1)
	case KeyUp:
		return c.move(-7)
	case KeyDown:
		return c.move(7)
	case KeyPageUp:
		return c.shiftMonth(-1)
	case KeyPageDown:
		return c.shiftMonth(1)
	case KeyToday:
		same := c.Month.Equal(time.Date(c.Today.Year(), c.Today.Month(), 1, 0, 0, 0, 0, time.UTC))
		c.Month, c.Sel = time.Date(c.Today.Year(), c.Today.Month(), 1, 0, 0, 0, 0, time.UTC), c.Today.Day()
		if same {
			return CalReq{}
		}
		c.Data, c.Stale = nil, false
		return CalReq{Kind: CalLoadMonth, Month: c.MonthKey()}
	case KeyEnter:
		c.Mode, c.Focus, c.Day, c.EntrySel = CalDayPanel, FocusInput, nil, 0
		c.Draft, c.Editing, c.ConfirmID, c.Busy = "", 0, 0, true
		return CalReq{Kind: CalLoadDay, Date: c.SelDate().Format(time.DateOnly)}
	case KeyEsc:
		return CalReq{Kind: CalClose}
	}
	return CalReq{}
}

func (c *Calendar) entries() []agent.CalEntry {
	if c.Day == nil {
		return nil
	}
	return c.Day.Entries
}

func (c *Calendar) dayKey(k CalKey) CalReq {
	es := c.entries()
	if c.ConfirmID != 0 { // delete confirmation: Enter = yes, anything else = no
		id := c.ConfirmID
		c.ConfirmID = 0
		if k == KeyEnter {
			c.Busy = true
			return CalReq{Kind: CalDelete, ID: id}
		}
		return CalReq{}
	}
	if c.Busy && k == KeyEnter {
		return CalReq{} // one request at a time: a second Enter must not save twice
	}
	switch k {
	case KeyEsc:
		if c.Draft != "" || c.Editing != 0 {
			c.Draft, c.Editing = "", 0
			return CalReq{}
		}
		c.Mode, c.Day = CalGrid, nil
		return CalReq{Kind: CalLoadMonth, Month: c.MonthKey()} // refresh the counts
	case KeyBackspace:
		if c.Focus == FocusInput && c.Draft != "" {
			_, n := utf8.DecodeLastRuneInString(c.Draft)
			c.Draft = c.Draft[:len(c.Draft)-n]
		}
	case KeyUp:
		if c.Focus == FocusInput && len(es) > 0 && c.Draft == "" {
			c.Focus, c.EntrySel = FocusList, len(es)-1
		} else if c.Focus == FocusList && c.EntrySel > 0 {
			c.EntrySel--
		}
	case KeyDown:
		if c.Focus == FocusList {
			if c.EntrySel < len(es)-1 {
				c.EntrySel++
			} else {
				c.Focus = FocusInput
			}
		}
	case KeyEdit:
		if c.Focus == FocusList && c.EntrySel < len(es) {
			c.Editing, c.Draft, c.Focus = es[c.EntrySel].ID, es[c.EntrySel].Text, FocusInput
		}
	case KeyDelete:
		if c.Focus == FocusList && c.EntrySel < len(es) {
			c.ConfirmID = es[c.EntrySel].ID
		}
	case KeyEnter:
		text := strings.TrimSpace(c.Draft)
		if c.Focus != FocusInput || text == "" {
			return CalReq{}
		}
		c.Busy = true
		if c.Editing != 0 {
			return CalReq{Kind: CalEdit, ID: c.Editing, Text: text}
		}
		return CalReq{Kind: CalAdd, Date: c.SelDate().Format(time.DateOnly), Text: text}
	case KeyLeft, KeyRight, KeyPageUp, KeyPageDown, KeyToday:
		// no meaning in the day panel (T is a letter there: see Type)
	}
	return CalReq{}
}

// Type adds typed characters in the day panel. In the grid, T is "today"
// and E/other letters do nothing; in the list, E edits and the delete key is
// handled by Key.
func (c *Calendar) Type(rs []rune) CalReq {
	if c.Mode == CalGrid {
		for _, r := range rs {
			if r == 't' || r == 'T' {
				return c.Key(KeyToday)
			}
		}
		return CalReq{}
	}
	if c.ConfirmID != 0 {
		return CalReq{}
	}
	if c.Focus == FocusList {
		if len(rs) == 1 && (rs[0] == 'e' || rs[0] == 'E') {
			return c.dayKey(KeyEdit)
		}
		c.Focus = FocusInput // typing anything else starts a new entry
	}
	for _, r := range rs {
		if r < ' ' && r != '\n' {
			continue
		}
		if utf8.RuneCountInString(c.Draft) >= MaxCalDraft {
			break
		}
		c.Draft += string(r)
	}
	return CalReq{}
}

// Paste inserts clipboard text (several lines become several entries).
func (c *Calendar) Paste(text string) {
	if c.Mode != CalDayPanel || c.ConfirmID != 0 {
		return
	}
	c.Focus = FocusInput
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if left := MaxCalDraft - utf8.RuneCountInString(c.Draft); left > 0 {
		r := []rune(text)
		c.Draft += string(r[:min(left, len(r))])
	}
}

// Done applies the outcome of an add, edit or delete: ok clears the input
// and asks for a reload of the day (the caller issues CalLoadDay).
func (c *Calendar) Done(kind CalReqKind, err error, queued bool) {
	c.Busy = false
	switch {
	case err != nil:
		c.Note = "Gagal: " + err.Error()
	case kind == CalAdd:
		c.Draft, c.Editing = "", 0
		c.Note = "Tersimpan"
		if queued {
			c.Note = "Tersimpan di laptop, dikirim saat online"
		}
	case kind == CalEdit:
		c.Draft, c.Editing = "", 0
		c.Note = "Diubah"
	case kind == CalDelete:
		c.Note = "Dihapus"
	}
}

// ReloadDay is the request that refreshes the day panel.
func (c *Calendar) ReloadDay() CalReq {
	c.Busy = true
	return CalReq{Kind: CalLoadDay, Date: c.SelDate().Format(time.DateOnly)}
}

// ---- layout ----

// CalLayout is where everything of the grid goes.
type CalLayout struct {
	Card     Rect
	Title    Rect
	PrevHit  Rect // the "<" arrow
	NextHit  Rect // the ">" arrow
	Head     []Rect
	Cells    map[int]Rect // day number → rect
	Rows     int
	Footer   Rect
	CellW    float64
	FirstCol int // 0 = Monday
}

// Layout computes the grid layout: Monday first, 5 or 6 rows.
func (c *Calendar) Layout() CalLayout {
	l := CalLayout{Card: Rect{X: CalCardX, Y: CalCardY, W: CalCardW, H: CalCardH}, Cells: map[int]Rect{}}
	x0, y0 := l.Card.X+calPad, l.Card.Y+calPad
	w := l.Card.W - 2*calPad
	l.CellW = w / 7
	l.Title = Rect{X: x0, Y: y0, W: w, H: calTitleH}
	l.PrevHit = Rect{X: x0, Y: y0, W: 28, H: calTitleH}
	l.NextHit = Rect{X: x0 + w - 28, Y: y0, W: 28, H: calTitleH}
	headY := y0 + calTitleH + 2
	for i := 0; i < 7; i++ {
		l.Head = append(l.Head, Rect{X: x0 + float64(i)*l.CellW, Y: headY, W: l.CellW, H: calHeadH})
	}
	l.FirstCol = (int(c.Month.Weekday()) + 6) % 7
	days := c.DaysInMonth()
	l.Rows = (l.FirstCol + days + 6) / 7
	gridY := headY + calHeadH + 2
	for d := 1; d <= days; d++ {
		i := l.FirstCol + d - 1
		l.Cells[d] = Rect{X: x0 + float64(i%7)*l.CellW, Y: gridY + float64(i/7)*calCellH, W: l.CellW, H: calCellH}
	}
	// the footer is anchored at the bottom of the card, so 5 and 6 rows both fit
	l.Footer = Rect{X: x0, Y: l.Card.Y + l.Card.H - calPad - calFooterH, W: w, H: calFooterH}
	return l
}

// Weekday headers, Monday first.
var CalHeads = [7]string{"Sen", "Sel", "Rab", "Kam", "Jum", "Sab", "Min"}

var calMonthNames = [12]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli", "Agustus", "September", "Oktober", "November", "Desember"}

// Title is "November 2026".
func (c *Calendar) Title() string {
	return calMonthNames[c.Month.Month()-1] + " " + itoa(c.Month.Year())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// DayMark is how a day is drawn.
type DayMark int

// Marks (drawn as dots and rings: the bubble font has no emoji).
const (
	MarkNone    DayMark = iota // an upcoming working day
	MarkFilled                 // has entries
	MarkMissed                 // a past working day without entries
	MarkHoliday                // national holiday, or marked off
	MarkLeave                  // cuti bersama
	MarkWeekend
)

// MarkOf classifies day d (1-based) of the shown month.
func (c *Calendar) MarkOf(d int) DayMark {
	date := time.Date(c.Month.Year(), c.Month.Month(), d, 0, 0, 0, 0, time.UTC)
	if c.Data == nil || d < 1 || d > len(c.Data.Days) {
		if wd := date.Weekday(); wd == time.Saturday || wd == time.Sunday {
			return MarkWeekend
		}
		return MarkNone
	}
	cd := c.Data.Days[d-1]
	working := cd.Kind == "workday" || cd.Kind == "leave" || cd.Kind == "work"
	switch {
	case cd.Count > 0:
		return MarkFilled
	case cd.Kind == "holiday" || cd.Kind == "off":
		return MarkHoliday
	case cd.Kind == "leave":
		return MarkLeave
	case cd.Kind == "weekend":
		return MarkWeekend
	case working && date.Before(c.Today):
		return MarkMissed
	}
	return MarkNone
}

// Summary is the line under the grid: "10/12 hari kerja terisi".
func (c *Calendar) Summary() string {
	if c.Data == nil {
		return "memuat..."
	}
	s := itoa(c.Data.Filled) + "/" + itoa(c.Data.Workdays) + " hari kerja terisi"
	if c.Stale {
		s += " (data lama)"
	}
	return s
}

// SelectedName is the holiday name of the selected day ("" if none).
func (c *Calendar) SelectedName() string {
	if c.Data == nil || c.Sel < 1 || c.Sel > len(c.Data.Days) {
		return ""
	}
	return c.Data.Days[c.Sel-1].HolidayName
}
