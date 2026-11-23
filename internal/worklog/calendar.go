package worklog

import (
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
)

// Kind is what a date is for the reminders and the calendar.
type Kind int

const (
	Workday Kind = iota
	Weekend
	Holiday // libur nasional, or a day the owner marked off (/libur)
	Leave   // cuti bersama: still a working day (reminded), only a marker
)

// HolidayInfo is a row of the holidays table.
type HolidayInfo struct {
	Name string
	Type string // "holiday" | "leave"
}

// Rules decide working days (PRD 6.5). Keys are "YYYY-MM-DD".
type Rules struct {
	Holidays  map[string][]HolidayInfo
	Overrides map[string]string // "off" | "work" (the owner's /libur, /kerja; they win)
}

func key(d time.Time) string { return d.Format(time.DateOnly) }

// Kind classifies d. Overrides win over the API data; a national holiday
// wins over cuti bersama on the same day.
func (r Rules) Kind(d time.Time) Kind {
	switch r.Overrides[key(d)] {
	case "off":
		return Holiday
	case "work":
		return Workday
	}
	leave := false
	for _, h := range r.Holidays[key(d)] {
		if h.Type == "holiday" {
			return Holiday
		}
		leave = leave || h.Type == "leave"
	}
	if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return Weekend
	}
	if leave {
		return Leave
	}
	return Workday
}

// IsWorkday: not a weekend, not a national holiday; cuti bersama counts as
// a working day; /kerja and /libur override.
func (r Rules) IsWorkday(d time.Time) bool {
	k := r.Kind(d)
	return k == Workday || k == Leave
}

// Name describes a holiday/leave/override on d ("" if none).
func (r Rules) Name(d time.Time) string {
	switch r.Overrides[key(d)] {
	case "off":
		return "libur (ditandai manual)"
	case "work":
		return "hari kerja (ditandai manual)"
	}
	var names []string
	for _, h := range r.Holidays[key(d)] {
		label := "libur nasional"
		if h.Type == "leave" {
			label = "cuti bersama"
		}
		names = append(names, label+": "+h.Name)
	}
	return strings.Join(names, "; ")
}

// Cell is one day of the month grid.
type Cell struct {
	Date    time.Time
	InMonth bool
	Mark    string // ✅ ⬜ 🔴 🟡 · ◉ or "" (an upcoming working day)
}

// MonthView is the calendar of one month.
type MonthView struct {
	Month    time.Time // first day, midnight UTC
	Cells    []Cell    // Monday-first weeks, padded to whole weeks
	Workdays int       // working days counted so far (see Completeness)
	Filled   int       // of those, how many have entries
	Missing  []time.Time
}

// BuildMonth lays out month (any day in it) for today. filled maps
// "YYYY-MM-DD" to the number of entries.
//
// Working days counted: every working day before today, plus today once it
// has an entry (so the ratio does not drop in the morning).
func BuildMonth(month, today time.Time, filled map[string]int, r Rules) MonthView {
	first := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	v := MonthView{Month: first}
	lead := (int(first.Weekday()) + 6) % 7 // Monday = 0
	for range lead {
		v.Cells = append(v.Cells, Cell{})
	}
	for d := first; d.Month() == first.Month(); d = d.AddDate(0, 0, 1) {
		n := filled[key(d)]
		k := r.Kind(d)
		mark := ""
		switch {
		case n > 0:
			mark = "✅"
		case d.Equal(today):
			mark = "◉"
		case k == Holiday:
			mark = "🔴"
		case k == Leave:
			mark = "🟡"
		case k == Weekend:
			mark = "·"
		case d.Before(today):
			mark = "⬜"
		}
		v.Cells = append(v.Cells, Cell{Date: d, InMonth: true, Mark: mark})
		if r.IsWorkday(d) && (d.Before(today) || (d.Equal(today) && n > 0)) {
			v.Workdays++
			if n > 0 {
				v.Filled++
			} else {
				v.Missing = append(v.Missing, d)
			}
		}
	}
	for len(v.Cells)%7 != 0 {
		v.Cells = append(v.Cells, Cell{})
	}
	return v
}

// Callback data (≤ 64 bytes; checked in tests).
const (
	CbNoop       = "nop"
	CbCancelWait = "pnc"
)

func cbMonth(m time.Time) string { return "cal:" + m.Format("2006-01") }
func cbDay(d time.Time) string   { return "day:" + key(d) }
func cbAdd(d time.Time) string   { return "add:" + key(d) }
func cbEdit(id int64) string     { return fmt.Sprintf("edt:%d", id) }
func cbDelete(id int64) string   { return fmt.Sprintf("del:%d", id) }
func cbDeleteYes(id int64) string {
	return fmt.Sprintf("dly:%d", id)
}

var monthNames = [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli",
	"Agustus", "September", "Oktober", "November", "Desember"}

// MonthTitle is "November 2026".
func MonthTitle(m time.Time) string { return fmt.Sprintf("%s %d", monthNames[m.Month()-1], m.Year()) }

// blank is a button label that shows nothing (Telegram rejects empty text).
const blank = "⠀"

// Text is the message text above the calendar keyboard.
func (v MonthView) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "📅 Worklog %s\n", MonthTitle(v.Month))
	if v.Workdays > 0 {
		fmt.Fprintf(&b, "✅ %d/%d hari kerja terisi\n", v.Filled, v.Workdays)
	} else {
		b.WriteString("Belum ada hari kerja yang lewat bulan ini.\n")
	}
	b.WriteString("✅ terisi · ⬜ kosong · 🔴 libur · 🟡 cuti bersama · ◉ hari ini\nKetuk tanggal untuk melihat atau mengubah isinya.")
	return b.String()
}

// Keyboard is the month grid: ◀ month ▶, weekday names, the days (each
// opens that day), and a footer.
func (v MonthView) Keyboard() [][]bot.Button {
	rows := [][]bot.Button{{
		{Text: "◀", Data: cbMonth(v.Month.AddDate(0, -1, 0))},
		{Text: MonthTitle(v.Month), Data: CbNoop},
		{Text: "▶", Data: cbMonth(v.Month.AddDate(0, 1, 0))},
	}}
	var head []bot.Button
	for _, n := range []string{"Sen", "Sel", "Rab", "Kam", "Jum", "Sab", "Min"} {
		head = append(head, bot.Button{Text: n, Data: CbNoop})
	}
	rows = append(rows, head)
	for i := 0; i < len(v.Cells); i += 7 {
		var row []bot.Button
		for _, c := range v.Cells[i : i+7] {
			if !c.InMonth {
				row = append(row, bot.Button{Text: blank, Data: CbNoop})
				continue
			}
			row = append(row, bot.Button{Text: fmt.Sprintf("%d%s", c.Date.Day(), c.Mark), Data: cbDay(c.Date)})
		}
		rows = append(rows, row)
	}
	return append(rows, []bot.Button{{Text: "📅 Hari ini", Data: "cal:now"}})
}

var dayNames = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}

// DayTitle is "Kamis 12 Nov 2026".
func DayTitle(d time.Time) string {
	return fmt.Sprintf("%s %d %s %d", dayNames[d.Weekday()], d.Day(), monthNames[d.Month()-1][:3], d.Year())
}

// EntryRef is an entry as the day view needs it.
type EntryRef struct {
	ID   int64
	Text string
}

// DayText is the day view: numbered bullets.
func DayText(d time.Time, entries []EntryRef, r Rules) string {
	var b strings.Builder
	b.WriteString("📝 " + DayTitle(d))
	if n := r.Name(d); n != "" {
		b.WriteString(" (" + n + ")")
	}
	if len(entries) == 0 {
		b.WriteString("\nBelum ada catatan.")
	}
	for i, e := range entries {
		fmt.Fprintf(&b, "\n%d. %s", i+1, e.Text)
	}
	return b.String()
}

// DayKeyboard has ✏️/🗑 per entry, then ➕ Tambah and ◀ Kembali.
func DayKeyboard(d time.Time, entries []EntryRef) [][]bot.Button {
	var rows [][]bot.Button
	for i, e := range entries {
		rows = append(rows, []bot.Button{
			{Text: fmt.Sprintf("✏️ Edit %d", i+1), Data: cbEdit(e.ID)},
			{Text: fmt.Sprintf("🗑 Hapus %d", i+1), Data: cbDelete(e.ID)},
		})
	}
	return append(rows, []bot.Button{
		{Text: "➕ Tambah", Data: cbAdd(d)},
		{Text: "◀ Kembali", Data: cbMonth(d)},
	})
}
