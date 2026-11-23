package worklog

import (
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/bot"
)

func rules() Rules {
	return Rules{
		Holidays: map[string][]HolidayInfo{
			"2026-11-17": {{Name: "Libur contoh", Type: "holiday"}},
			"2026-11-27": {{Name: "Cuti contoh", Type: "leave"}},
			"2026-11-28": {{Name: "Libur di hari Sabtu", Type: "holiday"}},
			"2026-11-30": {{Name: "Cuti contoh", Type: "leave"}, {Name: "Libur contoh 2", Type: "holiday"}},
		},
		Overrides: map[string]string{
			"2026-11-13": "off",  // a Friday the owner took off
			"2026-11-17": "work", // worked on the holiday
			"2026-11-21": "work", // a Saturday at work
		},
	}
}

func TestWorkdayRules(t *testing.T) {
	r := rules()
	cases := map[string]struct {
		kind    Kind
		workday bool
	}{
		"2026-11-16": {Workday, true},  // Monday
		"2026-11-14": {Weekend, false}, // Saturday
		"2026-11-15": {Weekend, false}, // Sunday
		"2026-11-17": {Workday, true},  // holiday overridden by /kerja
		"2026-11-27": {Leave, true},    // cuti bersama: still reminded
		"2026-11-28": {Holiday, false}, // holiday on a Saturday
		"2026-11-30": {Holiday, false}, // holiday wins over leave
		"2026-11-13": {Holiday, false}, // /libur
		"2026-11-21": {Workday, true},  // Saturday with /kerja
	}
	for day, want := range cases {
		d := date(day)
		if k := r.Kind(d); k != want.kind || r.IsWorkday(d) != want.workday {
			t.Errorf("%s: kind %d workday %v, want %d %v", day, k, r.IsWorkday(d), want.kind, want.workday)
		}
	}
	if r.Name(date("2026-11-27")) != "cuti bersama: Cuti contoh" || r.Name(date("2026-11-13")) != "libur (ditandai manual)" {
		t.Fatal("names")
	}
}

func TestBuildMonth(t *testing.T) {
	filled := map[string]int{"2026-11-02": 2, "2026-11-03": 1, "2026-11-08": 1, "2026-11-18": 3}
	today := date("2026-11-18")
	v := BuildMonth(today, today, filled, rules())
	marks := map[int]string{}
	for _, c := range v.Cells {
		if c.InMonth {
			marks[c.Date.Day()] = c.Mark
		}
	}
	want := map[int]string{
		1: "·", 2: "✅", 3: "✅", 4: "⬜", 8: "✅", // a weekend with entries is ✅
		13: "🔴", 14: "·", 17: "⬜", // 17: holiday overridden to work, empty → missing
		18: "✅", 19: "", 27: "🟡", 28: "🔴", 30: "🔴",
	}
	for d, m := range want {
		if marks[d] != m {
			t.Errorf("day %d mark %q, want %q", d, marks[d], m)
		}
	}
	if len(v.Cells)%7 != 0 || v.Cells[0].InMonth || v.Cells[6].Date.Day() != 1 {
		t.Fatal("grid must start on Monday (1 Nov 2026 is a Sunday)")
	}
	// working days up to today: 2-6, 9-12 (13 off), 16, 17 (override), and 18 (filled today) = 12
	if v.Workdays != 12 || v.Filled != 3 || len(v.Missing) != 9 {
		t.Fatalf("workdays=%d filled=%d missing=%d", v.Workdays, v.Filled, len(v.Missing))
	}
	// today without an entry is not counted yet (◉, no drop in the morning)
	v2 := BuildMonth(today, today, map[string]int{}, rules())
	if v2.Workdays != 11 || !strings.Contains(v2.Text(), "0/11") {
		t.Fatalf("today not filled: %d workdays, text %q", v2.Workdays, v2.Text())
	}
	for _, c := range v2.Cells {
		if c.InMonth && c.Date.Equal(today) && c.Mark != "◉" {
			t.Fatal("today is ◉")
		}
	}
}

func allButtons(rows [][]bot.Button) []bot.Button {
	var out []bot.Button
	for _, r := range rows {
		out = append(out, r...)
	}
	return out
}

func TestKeyboardsCallbackData(t *testing.T) {
	today := date("2026-11-18")
	v := BuildMonth(today, today, map[string]int{"2026-11-02": 1}, rules())
	kb := v.Keyboard()
	if len(kb) < 7 || len(kb[1]) != 7 {
		t.Fatalf("keyboard shape %d rows", len(kb))
	}
	var refs []EntryRef
	for i := range 30 {
		refs = append(refs, EntryRef{ID: 9_000_000_000_000 + int64(i), Text: "x"})
	}
	all := append(allButtons(kb), allButtons(DayKeyboard(today, refs))...)
	seen := map[string]bool{}
	for _, b := range all {
		if len(b.Data) == 0 || len(b.Data) > 64 {
			t.Errorf("callback_data %q is %d bytes", b.Data, len(b.Data))
		}
		if strings.TrimSpace(b.Text) == "" {
			t.Errorf("button with empty text (Telegram rejects it): %+v", b)
		}
		seen[b.Data] = true
	}
	for _, want := range []string{"cal:2026-10", "cal:2026-12", "day:2026-11-02", "add:2026-11-18", "edt:9000000000000", "del:9000000000029", "cal:now"} {
		if !seen[want] {
			t.Errorf("missing callback %q", want)
		}
	}
	// January → previous December of the year before
	jan := BuildMonth(date("2027-01-10"), today, nil, Rules{})
	if jan.Keyboard()[0][0].Data != "cal:2026-12" || MonthTitle(jan.Month) != "Januari 2027" {
		t.Fatal("month navigation across the year")
	}
}

func TestDayText(t *testing.T) {
	got := DayText(date("2026-11-27"), []EntryRef{{1, "rapat"}, {2, "deploy"}}, rules())
	if got != "📝 Jumat 27 Nov 2026 (cuti bersama: Cuti contoh)\n1. rapat\n2. deploy" {
		t.Fatalf("day text %q", got)
	}
	if !strings.Contains(DayText(date("2026-11-19"), nil, rules()), "Belum ada catatan") {
		t.Fatal("empty day")
	}
}
