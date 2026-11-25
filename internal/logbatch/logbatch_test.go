package logbatch

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var today = time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) // a Wednesday

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

// texts flattens a batch to "date text" lines for comparison.
func texts(b Batch) string {
	var out []string
	for _, e := range b.Entries {
		out = append(out, e.Date.Format("01-02")+" "+e.Text)
	}
	return strings.Join(out, "|")
}

func TestParseTable(t *testing.T) {
	cases := []struct {
		name, in, want string
		dated          bool
	}{
		{"one line", "rapat sprint", "11-25 rapat sprint", false},
		{"every bullet form", "- a\n* b\n• c\n1. d\n2) e\n10. f\nplain g", "11-25 a|11-25 b|11-25 c|11-25 d|11-25 e|11-25 f|11-25 plain g", false},
		{"bullet only inside text stays", "- 5-3=2 benar\n3.5 jam kerja\n-minus tanpa spasi", "11-25 5-3=2 benar|11-25 3.5 jam kerja|11-25 -minus tanpa spasi", false},
		{"blank lines and CRLF", "a\r\n\r\n   \r\nb\r\n", "11-25 a|11-25 b", false},
		{"inner spaces collapse", "  satu   dua \t tiga ", "11-25 satu dua tiga", false},
		{"lone bullet sign is skipped", "-\n*\na", "11-25 a", false},
		{"marker line sets the date below", "2026-11-23\n- a\n- b\nkemarin\n- c\nhari ini\n- d", "11-23 a|11-23 b|11-24 c|11-25 d", true},
		{"entries before a marker use today", "x\n2026-11-20\ny", "11-25 x|11-20 y", true},
		{"inline marker on the first line", "kemarin perbaiki bug login", "11-24 perbaiki bug login", true},
		{"inline iso on the first line", "2026-11-20 deploy ke staging", "11-20 deploy ke staging", true},
		{"inline hari ini", "hari ini rapat", "11-25 rapat", true},
		{"inline marker only on the first line", "a\nkemarin b", "11-25 a|11-25 kemarin b", false},
		{"marker words are case-insensitive", "KEMARIN\nrapat", "11-24 rapat", true},
		{"a bullet that says kemarin is an entry", "- kemarin", "11-25 kemarin", false},
		{"a date marker inside a sentence is text", "deploy 2026-11-20 selesai", "11-25 deploy 2026-11-20 selesai", false},
	}
	for _, c := range cases {
		b, err := Parse(c.in, today, time.Time{})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := texts(b); got != c.want || b.Dated != c.dated {
			t.Errorf("%s:\n got %q dated=%v\nwant %q dated=%v", c.name, got, b.Dated, c.want, c.dated)
		}
	}
}

func TestParseDefaultDate(t *testing.T) {
	b, err := Parse("a\nb", today, d("2026-11-10")) // "Tambah" on a past day
	if err != nil || texts(b) != "11-10 a|11-10 b" || b.Dated {
		t.Fatalf("default date: %v %v", texts(b), err)
	}
}

func TestParseRejections(t *testing.T) {
	long := strings.Repeat("x", MaxTextLen+1)
	many := strings.TrimSuffix(strings.Repeat("baris\n", MaxEntries+1), "\n")
	for name, in := range map[string]string{
		"empty":             "",
		"only blanks":       " \n\n\t",
		"only markers":      "kemarin\n2026-11-20",
		"future marker":     "a\n2026-11-26\nb",
		"future inline":     "2026-12-01 nanti",
		"entry too long":    "a\n" + long,
		"too long behind -": "- " + long,
		"51 entries":        many,
	} {
		if b, err := Parse(in, today, time.Time{}); err == nil {
			t.Errorf("%s: accepted (%d entries)", name, len(b.Entries))
		}
	}
	// the limits are inclusive
	if b, err := Parse(strings.Repeat("é", MaxTextLen), today, time.Time{}); err != nil || len(b.Entries) != 1 {
		t.Fatalf("2000 characters (not bytes) are allowed: %v", err)
	}
	if b, err := Parse(strings.TrimSuffix(strings.Repeat("baris\n", MaxEntries), "\n"), today, time.Time{}); err != nil || len(b.Entries) != MaxEntries {
		t.Fatalf("50 entries are allowed: %v", err)
	}
	// the message names the line and is in Indonesian
	_, err := Parse("a\nb\n2026-11-26\nc", today, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "baris 3") || !strings.Contains(err.Error(), "belum terjadi") {
		t.Fatalf("error text = %v", err)
	}
	_, err = Parse(strings.Repeat("baris\n", 60), today, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "maks 50") {
		t.Fatalf("too many: %v", err)
	}
}

func TestPreviewRules(t *testing.T) {
	ten, _ := Parse(strings.TrimSuffix(strings.Repeat("a\n", SaveDirect), "\n"), today, time.Time{})
	eleven, _ := Parse(strings.TrimSuffix(strings.Repeat("a\n", SaveDirect+1), "\n"), today, time.Time{})
	dated, _ := Parse("kemarin\na", today, time.Time{})
	if ten.NeedsPreview() || !eleven.NeedsPreview() || !dated.NeedsPreview() {
		t.Fatalf("≤ 10 and undated saves at once; > 10 or dated gets a preview: %v %v %v",
			ten.NeedsPreview(), eleven.NeedsPreview(), dated.NeedsPreview())
	}
	b, _ := Parse("2026-11-24\na\nb\n2026-11-25\nc\n2026-11-24\nd", today, time.Time{})
	days := b.Days()
	if len(days) != 2 || days[0].Count != 3 || days[1].Count != 1 || !days[0].Date.Equal(d("2026-11-24")) {
		t.Fatalf("days = %+v", days)
	}
	got := b.Preview(func(t time.Time) string { return fmt.Sprintf("%d Nov", t.Day()) })
	if got != "4 catatan: 24 Nov (3), 25 Nov (1)" {
		t.Fatalf("preview = %q", got)
	}
}

func TestDeriveID(t *testing.T) {
	a, b := DeriveID("paket-1", 0), DeriveID("paket-1", 1)
	if a == b || a != DeriveID("paket-1", 0) || a == DeriveID("paket-2", 0) {
		t.Fatalf("ids must be stable per (paket, line) and differ otherwise: %s %s", a, b)
	}
	if len(a) != 36 || a[14] != '5' || strings.IndexByte("89ab", a[19]) < 0 {
		t.Fatalf("not a UUID v5: %s", a)
	}
}
