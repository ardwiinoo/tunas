// Package reminders parses reminder times and runs the delivery scheduler.
package reminders

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxTextLen bounds reminder text (runes).
const MaxTextLen = 500

// Parsed is the result of parsing a /remind argument.
type Parsed struct {
	At         time.Time
	Recurrence string // "" | daily | weekdays | weekly:MON..SUN
	Text       string
}

// Usage is shown when parsing fails.
const Usage = `Format waktu:
• 17:00 teks
• besok 09:00 teks / lusa 09:00 teks
• +30m teks / +2j teks
• 2026-12-01 09:00 teks
• tiap hari 07:00 teks
• tiap hari kerja 07:00 teks
• tiap senin 08:00 teks`

var (
	reClock    = regexp.MustCompile(`^(\d{1,2})[:.](\d{2})$`)
	reRelative = regexp.MustCompile(`^\+(?:(\d+)j)?(?:(\d+)m)?$`)
	reDate     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
)

var weekdayCodes = map[string]string{
	"senin": "MON", "selasa": "TUE", "rabu": "WED", "kamis": "THU",
	"jumat": "FRI", "jum'at": "FRI", "sabtu": "SAT", "minggu": "SUN", "ahad": "SUN",
}

var codeWeekday = map[string]time.Weekday{
	"MON": time.Monday, "TUE": time.Tuesday, "WED": time.Wednesday, "THU": time.Thursday,
	"FRI": time.Friday, "SAT": time.Saturday, "SUN": time.Sunday,
}

const maxRelative = 366 * 24 * time.Hour

const weeklyPrefix = "weekly:"

// Parse interprets input relative to now (in loc).
func Parse(input string, now time.Time, loc *time.Location) (Parsed, error) {
	now = now.In(loc)
	tok, rest := cut(input)
	low := strings.ToLower(tok)
	var p Parsed

	switch {
	case tok == "":
		return p, errors.New("waktu dan teks pengingat kosong")

	case strings.HasPrefix(tok, "+"):
		d, err := parseRelative(low)
		if err != nil {
			return p, err
		}
		p.At = now.Add(d)

	case low == "besok" || low == "lusa":
		days := 1
		if low == "lusa" {
			days = 2
		}
		var clk string
		clk, rest = cut(rest)
		h, m, err := parseClock(clk)
		if err != nil {
			return p, err
		}
		y, mo, d := now.Date()
		p.At = time.Date(y, mo, d+days, h, m, 0, 0, loc)

	case reDate.MatchString(tok):
		var clk string
		clk, rest = cut(rest)
		h, m, err := parseClock(clk)
		if err != nil {
			return p, err
		}
		at, err := parseDate(tok, h, m, loc)
		if err != nil {
			return p, err
		}
		p.At = at

	case low == "tiap":
		var word string
		word, rest = cut(rest)
		word = strings.ToLower(word)
		switch {
		case word == "hari":
			p.Recurrence = "daily"
			if next, after := cut(rest); strings.EqualFold(next, "kerja") {
				p.Recurrence, rest = "weekdays", after
			}
		case weekdayCodes[word] != "":
			p.Recurrence = weeklyPrefix + weekdayCodes[word]
		default:
			return p, fmt.Errorf("pola berulang %q tidak dikenal (pakai: tiap hari, tiap hari kerja, tiap senin..minggu)", word)
		}
		var clk string
		clk, rest = cut(rest)
		h, m, err := parseClock(clk)
		if err != nil {
			return p, err
		}
		p.At = firstOccurrence(p.Recurrence, h, m, now)

	case reClock.MatchString(tok):
		h, m, err := parseClock(tok)
		if err != nil {
			return p, err
		}
		y, mo, d := now.Date()
		p.At = time.Date(y, mo, d, h, m, 0, 0, loc)
		if !p.At.After(now) {
			p.At = p.At.AddDate(0, 0, 1)
		}

	default:
		return p, fmt.Errorf("waktu %q tidak dikenali", tok)
	}

	if !p.At.After(now) {
		return p, errors.New("waktu sudah lewat")
	}
	p.Text = strings.TrimSpace(rest)
	if p.Text == "" {
		return p, errors.New("teks pengingat kosong")
	}
	if utf8.RuneCountInString(p.Text) > MaxTextLen {
		return p, fmt.Errorf("teks pengingat terlalu panjang (maks %d karakter)", MaxTextLen)
	}
	return p, nil
}

// cut splits off the first whitespace-delimited token.
func cut(s string) (tok, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

func parseClock(s string) (int, int, error) {
	m := reClock.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, fmt.Errorf("jam %q tidak valid (pakai HH:MM)", s)
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	if h > 23 || mi > 59 {
		return 0, 0, fmt.Errorf("jam %q tidak valid", s)
	}
	return h, mi, nil
}

func parseRelative(s string) (time.Duration, error) {
	m := reRelative.FindStringSubmatch(s)
	if m == nil || (m[1] == "" && m[2] == "") {
		return 0, fmt.Errorf("durasi %q tidak valid (contoh: +30m, +2j, +1j30m)", s)
	}
	var d time.Duration
	if m[1] != "" {
		h, err := strconv.Atoi(m[1])
		if err != nil || h > 366*24 {
			return 0, errors.New("durasi terlalu panjang")
		}
		d += time.Duration(h) * time.Hour
	}
	if m[2] != "" {
		mi, err := strconv.Atoi(m[2])
		if err != nil || mi > 366*24*60 {
			return 0, errors.New("durasi terlalu panjang")
		}
		d += time.Duration(mi) * time.Minute
	}
	if d <= 0 {
		return 0, errors.New("durasi harus lebih dari nol")
	}
	if d > maxRelative {
		return 0, errors.New("durasi terlalu panjang (maks 366 hari)")
	}
	return d, nil
}

func parseDate(s string, h, mi int, loc *time.Location) (time.Time, error) {
	m := reDate.FindStringSubmatch(s)
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(m[3])
	t := time.Date(y, time.Month(mo), d, h, mi, 0, 0, loc)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d {
		return time.Time{}, fmt.Errorf("tanggal %q tidak valid", s)
	}
	return t, nil
}

func matches(rec string, wd time.Weekday) bool {
	switch {
	case rec == "daily":
		return true
	case rec == "weekdays":
		return wd >= time.Monday && wd <= time.Friday
	case strings.HasPrefix(rec, weeklyPrefix):
		w, ok := codeWeekday[strings.TrimPrefix(rec, weeklyPrefix)]
		return ok && w == wd
	}
	return false
}

// firstOccurrence returns the first time > now at h:m that matches rec.
func firstOccurrence(rec string, h, m int, now time.Time) time.Time {
	y, mo, d := now.Date()
	for i := 0; i < 8; i++ {
		c := time.Date(y, mo, d+i, h, m, 0, 0, now.Location())
		if c.After(now) && matches(rec, c.Weekday()) {
			return c
		}
	}
	return time.Time{}
}

// ValidRecurrence reports whether rec is a supported recurrence value.
func ValidRecurrence(rec string) bool {
	if rec == "daily" || rec == "weekdays" {
		return true
	}
	code, ok := strings.CutPrefix(rec, weeklyPrefix)
	_, known := codeWeekday[code]
	return ok && known
}

// Next returns the next occurrence after both prev and now, keeping prev's
// local clock time. Missed occurrences during downtime are skipped (not replayed).
// Returns false for non-recurring or unknown rec.
func Next(rec string, prev, now time.Time, loc *time.Location) (time.Time, bool) {
	if !ValidRecurrence(rec) {
		return time.Time{}, false
	}
	prev = prev.In(loc)
	base := prev
	if now.After(prev) {
		base = now.In(loc)
	}
	t := firstOccurrence(rec, prev.Hour(), prev.Minute(), base)
	return t, !t.IsZero()
}

var (
	dayNames   = [...]string{"Min", "Sen", "Sel", "Rab", "Kam", "Jum", "Sab"}
	monthNames = [...]string{"", "Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	longDays   = map[string]string{"MON": "Senin", "TUE": "Selasa", "WED": "Rabu", "THU": "Kamis", "FRI": "Jumat", "SAT": "Sabtu", "SUN": "Minggu"}
)

// FormatDate renders t like "Sen 23 Nov" (year added if not the current year).
func FormatDate(t, now time.Time, loc *time.Location) string {
	t = t.In(loc)
	s := fmt.Sprintf("%s %d %s", dayNames[t.Weekday()], t.Day(), monthNames[t.Month()])
	if t.Year() != now.In(loc).Year() {
		s += fmt.Sprintf(" %d", t.Year())
	}
	return s
}

// FormatTime renders t like "Sen 23 Nov 17:00 WIB" (year added if not the current year).
func FormatTime(t, now time.Time, loc *time.Location) string {
	return FormatDate(t, now, loc) + " " + t.In(loc).Format("15:04 MST")
}

// DescribeRecurrence renders a recurrence for users ("" for one-off).
func DescribeRecurrence(rec string) string {
	switch {
	case rec == "daily":
		return "tiap hari"
	case rec == "weekdays":
		return "tiap hari kerja"
	case strings.HasPrefix(rec, weeklyPrefix):
		return "tiap " + longDays[strings.TrimPrefix(rec, weeklyPrefix)]
	}
	return ""
}
