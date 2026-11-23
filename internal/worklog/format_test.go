package worklog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var jkt, _ = time.LoadLocation("Asia/Jakarta")

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// Golden: every synthetic fixture in the old app's canonical form must
// survive Parse → Render byte for byte (line ends, blank lines, order).
func TestGoldenParseRender(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*_daily.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		res := Parse(string(raw))
		if res.Unreadable != 0 {
			t.Errorf("%s: %d unreadable lines", f, res.Unreadable)
		}
		if got := Render(res.Days); got != string(raw) {
			t.Errorf("%s: render differs\n--- got\n%q\n--- want\n%q", f, got, raw)
		}
	}
}

func TestParseTolerant(t *testing.T) {
	// CRLF, "*text" without a space, extra blank lines, a stray note line,
	// a repeated header: read the way the old app reads them
	in := "03/11/2026\r\n*rapat pagi\r\n\r\n\r\n* tulis laporan\r\ncatatan lepas\r\n05/11/2026\n* review\n03/11/2026\n* susulan\n"
	res := Parse(in)
	if len(res.Days) != 2 || res.Unreadable != 1 {
		t.Fatalf("days=%d unreadable=%d", len(res.Days), res.Unreadable)
	}
	if got := strings.Join(res.Days[0].Bullets, "|"); got != "rapat pagi|tulis laporan|susulan" {
		t.Fatalf("day 1 = %q", got)
	}
	want := "03/11/2026\n* rapat pagi\n* tulis laporan\n* susulan\n\n05/11/2026\n* review\n"
	if got := Render(res.Days); got != want {
		t.Fatalf("canonical form:\n%q\nwant\n%q", got, want)
	}
	if Render(nil) != "" || Render([]Day{{Date: date("2026-11-03")}}) != "" {
		t.Fatal("empty days render as an empty file")
	}
	if FileName(date("2026-11-23")) != "202611_daily.txt" || Month(date("2026-11-23")) != "2026-11" {
		t.Fatal("file name follows the old app (YYYYMM_daily.txt)")
	}
}

func TestNormalizeAndValidate(t *testing.T) {
	if got := NormalizeText("  * baris satu\r\nbaris  dua\n"); got != "baris satu baris dua" {
		t.Fatalf("normalize = %q", got)
	}
	if _, err := ValidateText("   "); err == nil {
		t.Fatal("empty text accepted")
	}
	if _, err := ValidateText(strings.Repeat("a", MaxTextLen+1)); err == nil {
		t.Fatal("too long text accepted")
	}
	if got, err := ValidateText(strings.Repeat("é", MaxTextLen)); err != nil || len([]rune(got)) != MaxTextLen {
		t.Fatal("limit is in characters, not bytes")
	}
}

func TestParseDateArg(t *testing.T) {
	// 00:30 local on Tue 24 Nov: still counted as the local day (24th)
	now := time.Date(2026, 11, 23, 17, 30, 0, 0, time.UTC)
	cases := []struct {
		in, date, rest string
		err            bool
	}{
		{"rapat sprint", "2026-11-24", "rapat sprint", false},
		{"kemarin perbaiki bug", "2026-11-23", "perbaiki bug", false},
		{"Kemarin perbaiki bug", "2026-11-23", "perbaiki bug", false},
		{"2026-11-20 deploy", "2026-11-20", "deploy", false},
		{"20/11/2026 deploy", "2026-11-20", "deploy", false},
		{"2026-11-25 besok", "", "", true},                          // future
		{"2026-13-01 aneh", "2026-11-24", "2026-13-01 aneh", false}, // not a date: all text
		{"", "2026-11-24", "", false},
	}
	for _, c := range cases {
		d, rest, err := ParseDateArg(c.in, now, jkt)
		if (err != nil) != c.err {
			t.Errorf("%q: err %v", c.in, err)
			continue
		}
		if err == nil && (d.Format(time.DateOnly) != c.date || rest != c.rest) {
			t.Errorf("%q → %s %q, want %s %q", c.in, d.Format(time.DateOnly), rest, c.date, c.rest)
		}
	}
}
