// Package logbatch parses a pasted block of worklog text into entries
// (PRD Fase 8a). It is pure (no database, no Telegram) so the server and the
// laptop agent use the same rules.
//
// Rules:
//   - every non-empty line is one entry;
//   - a bullet prefix is dropped: "-", "*", "•", "1." and "1)";
//   - a line that only holds a date marker ("2026-11-24", "kemarin",
//     "hari ini") sets the date of the lines below it, until the next marker;
//   - the first line may also start with a marker and a text ("kemarin
//     rapat sprint"), the /log syntax of before;
//   - at most 50 entries per message and 2000 characters per entry; anything
//     over is rejected as a whole, never saved partly.
package logbatch

import (
	"crypto/sha1"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits (PRD 8a).
const (
	MaxEntries = 50
	MaxTextLen = 2000
	// SaveDirect is the largest batch saved without a preview (and only
	// when it carries no date marker).
	SaveDirect = 10
)

// Entry is one worklog bullet for a date (midnight UTC of the local day).
type Entry struct {
	Date time.Time
	Text string
	Line int // 1-based line of the pasted text, for error messages
}

// Batch is a parsed paste.
type Batch struct {
	Entries []Entry
	Dated   bool // the paste carried at least one date marker
}

// Day is the number of entries on one date, in order of appearance.
type Day struct {
	Date  time.Time
	Count int
}

// Days summarises the batch per date.
func (b Batch) Days() []Day {
	var out []Day
	for _, e := range b.Entries {
		if n := len(out); n > 0 && out[n-1].Date.Equal(e.Date) {
			out[n-1].Count++
			continue
		}
		found := false
		for i := range out {
			if out[i].Date.Equal(e.Date) {
				out[i].Count++
				found = true
				break
			}
		}
		if !found {
			out = append(out, Day{Date: e.Date, Count: 1})
		}
	}
	return out
}

// NeedsPreview: more than SaveDirect entries, or any date marker.
func (b Batch) NeedsPreview() bool { return len(b.Entries) > SaveDirect || b.Dated }

var (
	reISODate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reBullet  = regexp.MustCompile(`^(?:[-*•]|\d{1,3}[.)])\s+`)
	reLoneBul = regexp.MustCompile(`^(?:[-*•]|\d{1,3}[.)])$`) // a bullet sign with no text
)

// marker reads a date marker; ok=false when s is not one.
func marker(s string, today time.Time) (time.Time, bool) {
	switch strings.ToLower(s) {
	case "kemarin":
		return today.AddDate(0, 0, -1), true
	case "hari ini":
		return today, true
	}
	if reISODate.MatchString(s) {
		if d, err := time.Parse(time.DateOnly, s); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

// inlineMarker reads "kemarin teks" / "2026-11-20 teks" on the first line.
func inlineMarker(s string, today time.Time) (time.Time, string, bool) {
	first, rest, ok := strings.Cut(s, " ")
	if !ok {
		return time.Time{}, "", false
	}
	if strings.EqualFold(first, "hari") { // "hari ini teks"
		if w, rest2, ok2 := strings.Cut(strings.TrimSpace(rest), " "); ok2 && strings.EqualFold(w, "ini") {
			return today, strings.TrimSpace(rest2), true
		}
		return time.Time{}, "", false
	}
	if d, ok := marker(first, today); ok {
		return d, strings.TrimSpace(rest), true
	}
	return time.Time{}, "", false
}

// stripBullet drops one bullet prefix and tidies the text.
func stripBullet(s string) string {
	s = strings.TrimSpace(s)
	if reLoneBul.MatchString(s) {
		return ""
	}
	if loc := reBullet.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	}
	return strings.Join(strings.Fields(s), " ")
}

// Parse splits text into entries. today is the local date (midnight UTC).
// Entries before any marker go to defaultDate (today when zero).
func Parse(text string, today, defaultDate time.Time) (Batch, error) {
	cur := defaultDate
	if cur.IsZero() {
		cur = today
	}
	var b Batch
	first := true
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		wasFirst := first
		first = false
		if d, ok := marker(line, today); ok {
			if d.After(today) {
				return Batch{}, fmt.Errorf("baris %d: tanggal %s belum terjadi", i+1, d.Format(time.DateOnly))
			}
			cur, b.Dated = d, true
			continue
		}
		if wasFirst {
			if d, rest, ok := inlineMarker(line, today); ok {
				if d.After(today) {
					return Batch{}, fmt.Errorf("baris %d: tanggal %s belum terjadi", i+1, d.Format(time.DateOnly))
				}
				cur, b.Dated, line = d, true, rest
			}
		}
		entry := stripBullet(line)
		if entry == "" {
			continue // a lone bullet sign
		}
		if n := utf8.RuneCountInString(entry); n > MaxTextLen {
			return Batch{}, fmt.Errorf("baris %d terlalu panjang (%d karakter, maks %d)", i+1, n, MaxTextLen)
		}
		b.Entries = append(b.Entries, Entry{Date: cur, Text: entry, Line: i + 1})
		if len(b.Entries) > MaxEntries {
			return Batch{}, fmt.Errorf("terlalu banyak catatan (maks %d per pesan); bagi jadi beberapa pesan", MaxEntries)
		}
	}
	if len(b.Entries) == 0 {
		return Batch{}, fmt.Errorf("tidak ada teks catatan")
	}
	return b, nil
}

// Preview is the short summary shown before saving: "12 catatan: 24 Nov (5), 25 Nov (7)".
func (b Batch) Preview(dayName func(time.Time) string) string {
	var parts []string
	for _, d := range b.Days() {
		parts = append(parts, fmt.Sprintf("%s (%d)", dayName(d.Date), d.Count))
	}
	return fmt.Sprintf("%d catatan: %s", len(b.Entries), strings.Join(parts, ", "))
}

// namespace is the UUID v5 namespace of derived entry ids (fixed forever:
// changing it would make a resent paste look new).
var namespace = [16]byte{0x7b, 0x4e, 0x2a, 0x91, 0x5c, 0x3d, 0x4f, 0x60, 0x8a, 0x17, 0xd2, 0x09, 0x6e, 0x41, 0xb3, 0x5a}

// DeriveID is the client_id of line number idx of a paste: a UUID v5 of the
// paste's own client_id and the index. Resending the same paste (the offline
// queue does) yields the same ids, so no entry is saved twice.
func DeriveID(parent string, idx int) string {
	h := sha1.New()
	h.Write(namespace[:])
	fmt.Fprintf(h, "%s:%d", parent, idx)
	sum := h.Sum(nil)[:16]
	sum[6] = sum[6]&0x0f | 0x50 // version 5
	sum[8] = sum[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
