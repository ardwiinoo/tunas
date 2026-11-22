package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

var jkt, _ = time.LoadLocation("Asia/Jakarta")

func hm(h, m int) time.Time { return time.Date(2026, 11, 23, h, m, 0, 0, jkt) }

func b(t time.Time, app, cat string, active int) store.Bucket {
	return store.Bucket{Start: t, App: app, Category: cat, ActiveSeconds: active}
}

func TestSummarize(t *testing.T) {
	var bs []store.Bucket
	// Code.exe 09:00-09:29 continuous (30 min), then 09:31-09:40 (gap 1 min → same session)
	for i := 0; i < 30; i++ {
		bs = append(bs, b(hm(9, i), "Code.exe", "work", 60))
	}
	for i := 31; i <= 40; i++ {
		bs = append(bs, b(hm(9, i), "Code.exe", "work", 50))
	}
	// Code.exe again at 10:00 (gap 19 min → new session)
	bs = append(bs, b(hm(10, 0), "Code.exe", "work", 60))
	// chrome 20 minutes with a 3 minute gap → two sessions of 10
	for i := 0; i < 10; i++ {
		bs = append(bs, b(hm(11, i), "chrome.exe", "browse", 30))
		bs = append(bs, b(hm(11, 13+i), "chrome.exe", "browse", 30))
	}
	bs = append(bs, b(hm(12, 0), "Discord.exe", "distract", 0)) // idle-only ignored

	s := Summarize(bs)
	wantTotal := (30*60 + 10*50 + 60 + 20*30) * time.Second
	if s.TotalActive != wantTotal {
		t.Fatalf("total = %v want %v", s.TotalActive, wantTotal)
	}
	if len(s.Apps) != 2 || s.Apps[0].App != "Code.exe" {
		t.Fatalf("apps = %+v", s.Apps)
	}
	if s.Longest == nil || s.Longest.App != "Code.exe" || s.Longest.Duration() != 41*time.Minute ||
		!s.Longest.Start.Equal(hm(9, 0)) {
		t.Fatalf("longest = %+v", s.Longest)
	}
	if s.ByCategory["browse"] != 10*time.Minute {
		t.Fatalf("by category = %v", s.ByCategory)
	}
}

func TestSessionGapBoundary(t *testing.T) {
	// end of 09:00 bucket = 09:01; next at 09:03 → gap exactly 2 min → same session
	s := Summarize([]store.Bucket{b(hm(9, 0), "a", "other", 10), b(hm(9, 3), "a", "other", 10)})
	if s.Longest.Duration() != 4*time.Minute {
		t.Fatalf("gap 2m should join: %v", s.Longest.Duration())
	}
	s = Summarize([]store.Bucket{b(hm(9, 0), "a", "other", 10), b(hm(9, 4), "a", "other", 10)})
	if s.Longest.Duration() != time.Minute {
		t.Fatalf("gap 3m should split: %v", s.Longest.Duration())
	}
}

func TestFormatToday(t *testing.T) {
	if got := FormatToday(Summary{}, "Hari ini", jkt); !strings.Contains(got, "Belum ada data") {
		t.Fatalf("empty: %q", got)
	}
	var bs []store.Bucket
	for i := 0; i < 7; i++ {
		bs = append(bs, b(hm(9, i), string(rune('a'+i))+".exe", "work", 60-i))
	}
	got := FormatToday(Summarize(bs), "Hari ini", jkt)
	for _, want := range []string{"Total aktif: 7 m", "5. e.exe", "Sesi terpanjang", "kerja: 7 m"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "6. ") {
		t.Error("more than 5 apps listed")
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "< 1 m", 20 * time.Second: "< 1 m", 45 * time.Minute: "45 m",
		2 * time.Hour: "2 j", 200 * time.Minute: "3 j 20 m",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q want %q", d, got, want)
		}
	}
}

func TestDayRange(t *testing.T) {
	// 2026-11-23 18:00 UTC is 2026-11-24 01:00 WIB
	from, to := DayRange(time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC), jkt)
	if !from.Equal(time.Date(2026, 11, 24, 0, 0, 0, 0, jkt)) || to.Sub(from) != 24*time.Hour {
		t.Fatalf("range = %v %v", from, to)
	}
}
