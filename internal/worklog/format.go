// Package worklog holds the daily work log: the monthly file format of the
// old worklog app (so the GitHub backup stays readable by it), working-day
// rules, the calendar and the date syntax of /log.
package worklog

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The old app (worklog, Go + Fyne) writes one file per month:
//
//	YYYYMM_daily.txt          e.g. 202611_daily.txt (no dash; see DECISIONS)
//
//	DD/MM/YYYY                a date header line
//	* first entry             one bullet per entry
//	* second entry
//	                          exactly one empty line between days
//	DD/MM/YYYY
//	* ...
//
// Days appear in the order they were first written (a new day is appended
// at the end), bullets in the order they were added, and the file ends
// with a single "\n". Lines inside a day that are not bullets are ignored
// by the old app; Parse counts them as unreadable.

const (
	dateLayout   = "02/01/2006"
	bulletPrefix = "* "
)

var reDateHeader = regexp.MustCompile(`^\d{2}/\d{2}/\d{4}\s*$`)

// Day is one date block of a monthly file.
type Day struct {
	Date    time.Time // midnight UTC of the local date
	Bullets []string
}

// FileName is the monthly file name for the month containing d.
func FileName(d time.Time) string { return d.Format("200601") + "_daily.txt" }

// Month is "YYYY-MM" for d.
func Month(d time.Time) string { return d.Format("2006-01") }

// ParseResult is the outcome of Parse.
type ParseResult struct {
	Days       []Day
	Unreadable int // non-empty lines that are neither a header nor a bullet in a day
}

// Parse reads a monthly file. Like the old app it accepts "*text" and
// "* text", CRLF line ends and extra blank lines; a header that repeats a
// date adds to that day.
func Parse(content string) ParseResult {
	var res ParseResult
	index := map[string]int{}
	cur := -1
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case reDateHeader.MatchString(t):
			d, err := time.Parse(dateLayout, t)
			if err != nil {
				res.Unreadable++
				cur = -1
				continue
			}
			key := d.Format(time.DateOnly)
			i, ok := index[key]
			if !ok {
				i = len(res.Days)
				index[key] = i
				res.Days = append(res.Days, Day{Date: d})
			}
			cur = i
		case strings.HasPrefix(t, "*") && cur >= 0:
			if b := strings.TrimSpace(strings.TrimPrefix(t, "*")); b != "" {
				res.Days[cur].Bullets = append(res.Days[cur].Bullets, b)
			}
		default:
			res.Unreadable++
		}
	}
	return res
}

// Render writes days in the given order in the old app's canonical form.
// Days without bullets are left out (the old app removes an emptied day).
func Render(days []Day) string {
	var blocks []string
	for _, d := range days {
		if len(d.Bullets) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString(d.Date.Format(dateLayout))
		for _, t := range d.Bullets {
			b.WriteString("\n" + bulletPrefix + t)
		}
		blocks = append(blocks, b.String())
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// MaxTextLen bounds one entry (runes), as in the database.
const MaxTextLen = 2000

// NormalizeText makes one entry out of free text: one entry = one bullet,
// so line breaks become spaces and a leading "*" is dropped.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	return strings.TrimSpace(strings.TrimPrefix(s, "*"))
}

// ParseDateArg reads the optional date at the start of /log arguments:
// "kemarin", "YYYY-MM-DD" or "DD/MM/YYYY"; otherwise the date is today.
// It returns the date (midnight UTC of the local date) and the rest.
// Future dates are rejected.
func ParseDateArg(args string, now time.Time, loc *time.Location) (time.Time, string, error) {
	today := LocalDate(now, loc)
	first, rest, _ := strings.Cut(strings.TrimSpace(args), " ")
	var d time.Time
	switch {
	case strings.EqualFold(first, "kemarin"):
		d = today.AddDate(0, 0, -1)
	case strings.EqualFold(first, "hari-ini"), strings.EqualFold(first, "hariini"):
		d = today
	default:
		var err error
		if d, err = time.Parse(time.DateOnly, first); err != nil {
			if d, err = time.Parse(dateLayout, first); err != nil {
				return today, strings.TrimSpace(args), nil // no date: all of it is the text
			}
		}
	}
	if d.After(today) {
		return time.Time{}, "", fmt.Errorf("tanggal %s belum terjadi", d.Format(time.DateOnly))
	}
	return d, strings.TrimSpace(rest), nil
}

// LocalDate is the calendar date of t in loc, as midnight UTC (the form
// dates are compared and stored in).
func LocalDate(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
