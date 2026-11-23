package worklog

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

const owner = int64(42)

func newService(t *testing.T) (*Service, *store.DB, *time.Time) {
	db := storetest.New(t)
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, jkt) // Wednesday
	s := &Service{DB: db, Loc: jkt, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler), ChatID: owner}
	return s, db, &now
}

func button(t *testing.T, r bot.Reply, prefix string) string {
	t.Helper()
	for _, row := range r.Rows {
		for _, b := range row {
			if strings.HasPrefix(b.Text, prefix) {
				return b.Data
			}
		}
	}
	t.Fatalf("no button %q in %+v", prefix, r.Rows)
	return ""
}

func cmd(t *testing.T, s *Service, c, args string) bot.Reply {
	t.Helper()
	r, ok := s.Command(context.Background(), c, args)
	if !ok {
		t.Fatalf("/%s not handled", c)
	}
	return r
}

func cb(t *testing.T, s *Service, data string) bot.Reply {
	t.Helper()
	r, ok := s.Callback(context.Background(), data)
	if !ok {
		t.Fatalf("callback %q not handled", data)
	}
	return r
}

func TestLogAndCalendar(t *testing.T) {
	s, db, _ := newService(t)
	ctx := context.Background()

	r := cmd(t, s, "log", "tes")
	if r.Text != "✅ Tersimpan (1 catatan hari ini)" {
		t.Fatalf("/log tes → %q", r.Text)
	}
	cmd(t, s, "log", "kemarin   rapat\nsprint")
	if r := cmd(t, s, "log", "2026-11-19 besok"); !strings.Contains(r.Text, "belum terjadi") {
		t.Fatalf("future date accepted: %q", r.Text)
	}
	if r := cmd(t, s, "log", strings.Repeat("x", MaxTextLen+1)); !strings.Contains(r.Text, "terlalu panjang") {
		t.Fatal("too long accepted")
	}
	if r := cmd(t, s, "log", ""); !strings.Contains(r.Text, "Pakai") {
		t.Fatal("usage")
	}
	es, _ := db.WorklogByDate(ctx, date("2026-11-17"))
	if len(es) != 1 || es[0].Text != "rapat sprint" || es[0].Source != "telegram" {
		t.Fatalf("kemarin entry = %+v", es)
	}

	// /cal shows ✅ on today and on yesterday
	cal := cmd(t, s, "cal", "")
	if !strings.Contains(cal.Text, "November 2026") {
		t.Fatalf("cal text %q", cal.Text)
	}
	day18 := button(t, cal, "18")
	if day18 != "day:2026-11-18" || !strings.Contains(fmtRows(cal), "18✅") || !strings.Contains(fmtRows(cal), "17✅") {
		t.Fatalf("calendar buttons %s", fmtRows(cal))
	}
	// tap the date: its entries
	day := cb(t, s, day18)
	if !strings.Contains(day.Text, "Rabu 18 Nov 2026") || !strings.Contains(day.Text, "1. tes") {
		t.Fatalf("day view %q", day.Text)
	}
	// month navigation edits in place
	if next := cb(t, s, "cal:2026-12"); !strings.Contains(next.Text, "Desember 2026") {
		t.Fatal("next month")
	}
	if nop := cb(t, s, CbNoop); !nop.Keep {
		t.Fatal("nop keeps the message")
	}
}

func fmtRows(r bot.Reply) string {
	var b strings.Builder
	for _, row := range r.Rows {
		for _, x := range row {
			b.WriteString(x.Text + " ")
		}
	}
	return b.String()
}

func TestEditAddDeleteUndo(t *testing.T) {
	s, db, _ := newService(t)
	ctx := context.Background()
	cmd(t, s, "log", "pertama")
	day := cb(t, s, "day:2026-11-18")

	// edit: button → text reply
	ask := cb(t, s, button(t, day, "✏️ Edit 1"))
	if !strings.Contains(ask.Text, "teks pengganti") {
		t.Fatalf("edit prompt %q", ask.Text)
	}
	if r, ok := s.PendingText(ctx, "pertama (diubah)"); !ok || r.Text != "✏️ Diubah." {
		t.Fatalf("edit reply %+v %v", r, ok)
	}
	es, _ := db.WorklogByDate(ctx, date("2026-11-18"))
	if es[0].Text != "pertama (diubah)" {
		t.Fatalf("edited = %q", es[0].Text)
	}
	if _, ok := s.PendingText(ctx, "obrolan biasa"); ok {
		t.Fatal("pending is single-use: the next text goes to the normal pipeline")
	}

	// add on a past day
	cb(t, s, "add:2026-11-16")
	if r, ok := s.PendingText(ctx, "susulan senin"); !ok || !strings.Contains(r.Text, "Senin 16 Nov 2026") {
		t.Fatalf("add reply %+v", r)
	}
	if r := cb(t, s, "add:2026-11-30"); !r.Keep || r.Toast == "" {
		t.Fatal("adding to a future day is refused")
	}
	// cancel waiting
	cb(t, s, "add:2026-11-18")
	if r := cb(t, s, CbCancelWait+":2026-11-18"); !strings.Contains(r.Text, "Rabu 18 Nov") {
		t.Fatal("cancel returns to the day")
	}
	if _, ok := s.PendingText(ctx, "x"); ok {
		t.Fatal("cancelled wait must not take text")
	}

	// delete asks first
	day = cb(t, s, "day:2026-11-18")
	confirm := cb(t, s, button(t, day, "🗑 Hapus 1"))
	if !strings.Contains(confirm.Text, "Hapus catatan ini?") {
		t.Fatal("delete asks")
	}
	if es, _ := db.WorklogByDate(ctx, date("2026-11-18")); len(es) != 1 {
		t.Fatal("nothing deleted before confirming")
	}
	after := cb(t, s, button(t, confirm, "🗑 Ya"))
	if after.Toast != "Dihapus" || !strings.Contains(after.Text, "Belum ada catatan") {
		t.Fatalf("after delete %+v", after)
	}

	// undo right after saving
	saved := cmd(t, s, "log", "salah ketik")
	undo := cb(t, s, button(t, saved, "↩️"))
	if !strings.Contains(undo.Text, "Dibatalkan") {
		t.Fatal("undo")
	}
	if es, _ := db.WorklogByDate(ctx, date("2026-11-18")); len(es) != 0 {
		t.Fatal("undo deletes the entry")
	}
	if r := cb(t, s, "edt:999999"); !strings.Contains(r.Text, "sudah tidak ada") {
		t.Fatal("missing entry")
	}
	// an expired wait is ignored
	cb(t, s, "add:2026-11-18")
	later := s.Now().Add(31 * time.Minute)
	s.Now = func() time.Time { return later }
	if _, ok := s.PendingText(ctx, "telat"); ok {
		t.Fatal("expired wait must not take text")
	}
}

func TestOverridesAndMonthOrder(t *testing.T) {
	s, db, now := newService(t)
	ctx := context.Background()
	if r := cmd(t, s, "libur", "2026-11-20 izin"); !strings.Contains(r.Text, "ditandai libur") {
		t.Fatalf("/libur %q", r.Text)
	}
	cmd(t, s, "kerja", "2026-11-21")
	rules, _ := s.Rules(ctx, date("2026-11-01"), date("2026-12-01"))
	if rules.IsWorkday(date("2026-11-20")) || !rules.IsWorkday(date("2026-11-21")) {
		t.Fatal("overrides")
	}
	if r := cmd(t, s, "libur", "hapus 2026-11-20"); !strings.Contains(r.Text, "dihapus") {
		t.Fatalf("remove override %q", r.Text)
	}
	if r := cmd(t, s, "libur", "hapus 2026-11-20"); !strings.Contains(r.Text, "tidak punya") {
		t.Fatal("removing twice")
	}

	// file order = order in which days were first written
	write := func(day, text string, at time.Time) {
		if _, err := db.AddWorklogAt(ctx, date(day), text, "import", at); err != nil {
			t.Fatal(err)
		}
	}
	base := now.Add(-time.Hour)
	write("2026-11-05", "a", base)
	write("2026-11-03", "b", base.Add(time.Minute)) // written after the 5th
	write("2026-11-05", "c", base.Add(2*time.Minute))
	es, err := db.WorklogMonth(ctx, date("2026-11-01"))
	if err != nil {
		t.Fatal(err)
	}
	var days []Day
	for _, e := range es {
		if len(days) == 0 || !days[len(days)-1].Date.Equal(e.Date) {
			days = append(days, Day{Date: e.Date})
		}
		days[len(days)-1].Bullets = append(days[len(days)-1].Bullets, e.Text)
	}
	if got := Render(days); got != "05/11/2026\n* a\n* c\n\n03/11/2026\n* b\n" {
		t.Fatalf("render order:\n%s", got)
	}
}
