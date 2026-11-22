package agentstatus

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

var jkt, _ = time.LoadLocation("Asia/Jakarta")

func TestDescribe(t *testing.T) {
	now := time.Date(2026, 11, 22, 20, 52, 0, 0, jkt)
	seen := now.Add(-12 * time.Second)
	d := store.Device{Name: "laptop", LastSeenAt: &seen, AgentVersion: "agent-v1.0.0"}
	got := Describe(d, 0, now, jkt)
	want := "Laptop: online (terakhir terlihat 12 detik lalu)\nVersi agen: agent-v1.0.0\nEvent tertunda: 0"
	if got != want {
		t.Fatalf("online:\n%s\nwant:\n%s", got, want)
	}

	off := time.Date(2026, 11, 22, 18, 42, 0, 0, jkt)
	d = store.Device{Name: "laptop", LastSeenAt: &off, TrackingPaused: true}
	got = Describe(d, 3, now, jkt)
	if !strings.HasPrefix(got, "Laptop: offline sejak 18:42 (2 jam 10 menit)") ||
		!strings.Contains(got, "Versi agen: tidak diketahui") || !strings.Contains(got, "Pelacakan: dijeda") ||
		!strings.HasSuffix(got, "Event tertunda: 3") {
		t.Fatalf("offline:\n%s", got)
	}

	if got := Describe(store.Device{Name: "pc"}, 0, now, jkt); !strings.HasPrefix(got, "Pc: belum pernah tersambung") {
		t.Fatal(got)
	}
}

func TestOnlineWindow(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) store.Device { s := now.Add(-d); return store.Device{LastSeenAt: &s} }
	if !Online(at(OnlineWindow), now) || Online(at(OnlineWindow+time.Second), now) || Online(store.Device{}, now) {
		t.Fatal("online window")
	}
}

func TestClockAndAgo(t *testing.T) {
	now := time.Date(2026, 11, 22, 9, 0, 0, 0, jkt)
	for in, want := range map[time.Time]string{
		time.Date(2026, 11, 22, 8, 5, 0, 0, jkt):      "08:05",
		time.Date(2026, 11, 21, 23, 0, 0, 0, jkt):     "kemarin 23:00",
		time.Date(2026, 11, 18, 7, 30, 0, 0, jkt):     "18 Nov 07:30",
		time.Date(2026, 11, 22, 1, 5, 0, 0, time.UTC): "08:05", // shown in the owner's zone
	} {
		if got := Clock(in, now, jkt); got != want {
			t.Errorf("Clock(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[time.Duration]string{
		12 * time.Second: "12 detik", 5 * time.Minute: "5 menit", 2 * time.Hour: "2 jam",
		130 * time.Minute: "2 jam 10 menit", 76 * time.Hour: "3 hari 4 jam", 72 * time.Hour: "3 hari",
	} {
		if got := Ago(in); got != want {
			t.Errorf("Ago(%v) = %q, want %q", in, got, want)
		}
	}
}
