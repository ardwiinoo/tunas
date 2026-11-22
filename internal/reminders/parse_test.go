package reminders

import (
	"testing"
	"time"
)

var jkt = mustLoc("Asia/Jakarta")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

func at(y int, mo time.Month, d, h, m int) time.Time { return time.Date(y, mo, d, h, m, 0, 0, jkt) }

func TestParse(t *testing.T) {
	mon10 := at(2026, 11, 23, 10, 0) // Monday 10:00 WIB
	late := at(2026, 11, 23, 23, 50) // Monday 23:50 WIB
	sat := at(2026, 11, 28, 12, 0)   // Saturday
	nye := at(2026, 12, 31, 23, 30)  // year boundary

	tests := []struct {
		name, in string
		now      time.Time
		want     time.Time
		rec      string
		text     string
	}{
		{"clock today", "17:00 kirim laporan", mon10, at(2026, 11, 23, 17, 0), "", "kirim laporan"},
		{"clock dot", "17.30 makan", mon10, at(2026, 11, 23, 17, 30), "", "makan"},
		{"clock passed -> tomorrow", "09:00 standup", mon10, at(2026, 11, 24, 9, 0), "", "standup"},
		{"clock equal now -> tomorrow", "10:00 x", mon10, at(2026, 11, 24, 10, 0), "", "x"},
		{"clock after midnight", "00:10 tidur", late, at(2026, 11, 24, 0, 10), "", "tidur"},
		{"besok", "besok 09:00 rapat", mon10, at(2026, 11, 24, 9, 0), "", "rapat"},
		{"Besok case", "Besok 9:05 rapat", mon10, at(2026, 11, 24, 9, 5), "", "rapat"},
		{"lusa", "lusa 08:00 bayar", mon10, at(2026, 11, 25, 8, 0), "", "bayar"},
		{"besok month end", "besok 07:00 x", at(2026, 11, 30, 20, 0), at(2026, 12, 1, 7, 0), "", "x"},
		{"besok year end", "besok 07:00 x", nye, at(2027, 1, 1, 7, 0), "", "x"},
		{"relative minutes", "+30m teh", mon10, at(2026, 11, 23, 10, 30), "", "teh"},
		{"relative hours", "+2j cek oven", mon10, at(2026, 11, 23, 12, 0), "", "cek oven"},
		{"relative combo", "+1j30m x", mon10, at(2026, 11, 23, 11, 30), "", "x"},
		{"relative past midnight", "+20m x", late, at(2026, 11, 24, 0, 10), "", "x"},
		{"absolute date", "2026-12-01 09:00 bayar listrik", mon10, at(2026, 12, 1, 9, 0), "", "bayar listrik"},
		{"tiap hari future today", "tiap hari 17:00 olahraga", mon10, at(2026, 11, 23, 17, 0), "daily", "olahraga"},
		{"tiap hari passed", "tiap hari 07:00 minum air", mon10, at(2026, 11, 24, 7, 0), "daily", "minum air"},
		{"tiap hari kerja on sat", "tiap hari kerja 07:00 absen", sat, at(2026, 11, 30, 7, 0), "weekdays", "absen"},
		{"tiap hari kerja mon", "tiap hari kerja 11:00 absen", mon10, at(2026, 11, 23, 11, 0), "weekdays", "absen"},
		{"tiap senin passed", "tiap senin 08:00 review", mon10, at(2026, 11, 30, 8, 0), "weekly:MON", "review"},
		{"tiap jumat", "tiap jumat 16:00 jumatan", mon10, at(2026, 11, 27, 16, 0), "weekly:FRI", "jumatan"},
		{"tiap Minggu", "tiap Minggu 06:00 lari", mon10, at(2026, 11, 29, 6, 0), "weekly:SUN", "lari"},
		{"keeps inner spacing", "17:00  a  b ", mon10, at(2026, 11, 23, 17, 0), "", "a  b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse(tt.in, tt.now, jkt)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if !p.At.Equal(tt.want) || p.Recurrence != tt.rec || p.Text != tt.text {
				t.Fatalf("Parse(%q) = %v %q %q, want %v %q %q", tt.in, p.At.In(jkt), p.Recurrence, p.Text, tt.want, tt.rec, tt.text)
			}
		})
	}
}

func TestParseUTCNowIsConverted(t *testing.T) {
	// 2026-11-23 16:30 UTC = 23:30 WIB; "23:45" must be the same WIB day.
	now := time.Date(2026, 11, 23, 16, 30, 0, 0, time.UTC)
	p, err := Parse("23:45 x", now, jkt)
	if err != nil {
		t.Fatal(err)
	}
	if want := at(2026, 11, 23, 23, 45); !p.At.Equal(want) {
		t.Fatalf("got %v want %v", p.At.In(jkt), want)
	}
}

func TestParseErrors(t *testing.T) {
	now := at(2026, 11, 23, 10, 0)
	for _, in := range []string{
		"", "17:00", "17:00   ", "25:00 x", "17:60 x", "jam5 x", "+0m x", "+m x", "+5d x",
		"+99999j x", "besok x", "besok", "2026-02-30 09:00 x", "2026-11-01 09:00 lewat",
		"tiap bulan 07:00 x", "tiap hari x", "tiap senin", "2026-12-01 x",
	} {
		if p, err := Parse(in, now, jkt); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", in, p)
		}
	}
	long := make([]byte, MaxTextLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Parse("17:00 "+string(long), now, jkt); err == nil {
		t.Error("expected too-long error")
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		name      string
		rec       string
		prev, now time.Time
		want      time.Time
	}{
		{"daily", "daily", at(2026, 11, 23, 7, 0), at(2026, 11, 23, 7, 0), at(2026, 11, 24, 7, 0)},
		{"daily skips downtime", "daily", at(2026, 11, 20, 7, 0), at(2026, 11, 23, 10, 0), at(2026, 11, 24, 7, 0)},
		{"daily downtime same day", "daily", at(2026, 11, 20, 17, 0), at(2026, 11, 23, 10, 0), at(2026, 11, 23, 17, 0)},
		{"weekdays fri->mon", "weekdays", at(2026, 11, 27, 7, 0), at(2026, 11, 27, 7, 0), at(2026, 11, 30, 7, 0)},
		{"weekly", "weekly:MON", at(2026, 11, 23, 8, 0), at(2026, 11, 23, 8, 0), at(2026, 11, 30, 8, 0)},
		{"year end", "daily", at(2026, 12, 31, 7, 0), at(2026, 12, 31, 7, 0), at(2027, 1, 1, 7, 0)},
	}
	for _, tt := range tests {
		got, ok := Next(tt.rec, tt.prev, tt.now, jkt)
		if !ok || !got.Equal(tt.want) {
			t.Errorf("%s: Next = %v %v, want %v", tt.name, got.In(jkt), ok, tt.want)
		}
	}
	if _, ok := Next("", at(2026, 11, 23, 7, 0), at(2026, 11, 23, 7, 0), jkt); ok {
		t.Error("one-off should not recur")
	}
	if _, ok := Next("weekly:XYZ", at(2026, 11, 23, 7, 0), at(2026, 11, 23, 7, 0), jkt); ok {
		t.Error("bad recurrence should not recur")
	}
}

func TestFormatTime(t *testing.T) {
	now := at(2026, 11, 22, 9, 0)
	if got := FormatTime(at(2026, 11, 23, 17, 0), now, jkt); got != "Sen 23 Nov 17:00 WIB" {
		t.Errorf("got %q", got)
	}
	if got := FormatTime(at(2027, 1, 1, 7, 5), now, jkt); got != "Jum 1 Jan 2027 07:05 WIB" {
		t.Errorf("got %q", got)
	}
	if DescribeRecurrence("weekly:MON") != "tiap Senin" || DescribeRecurrence("weekdays") != "tiap hari kerja" {
		t.Error("describe")
	}
}
