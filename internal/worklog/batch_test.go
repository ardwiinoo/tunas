package worklog

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

func entriesOn(t *testing.T, db *store.DB, day string) []string {
	t.Helper()
	es, err := db.WorklogByDate(context.Background(), date(day))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Text)
	}
	return out
}

// Pasting 8 lines gives 8 entries and one confirmation; "Batalkan semua"
// removes only that paste.
func TestLogPasteEightLines(t *testing.T) {
	s, db, _ := newService(t) // Wed 2026-11-18
	cmd(t, s, "log", "tulisan lama yang harus tetap ada")
	before := entriesOn(t, db, "2026-11-18")

	paste := "- rapat sprint\n* review PR\n• deploy staging\n1. perbaiki bug login\n2) tulis dokumentasi\n\n   \nupdate dependensi\nbalas email klien"
	r := cmd(t, s, "log", paste)
	if r.Text != "✅ 7 catatan tersimpan" {
		t.Fatalf("reply %q", r.Text)
	}
	got := entriesOn(t, db, "2026-11-18")
	want := append(append([]string(nil), before...), "rapat sprint", "review PR", "deploy staging", "perbaiki bug login", "tulis dokumentasi", "update dependensi", "balas email klien")
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("entries:\n got %q\nwant %q", got, want)
	}
	if len(r.Rows) != 1 || !strings.Contains(fmt.Sprint(r.Rows), "Batalkan semua") {
		t.Fatalf("buttons %+v", r.Rows)
	}
	undo := cb(t, s, button(t, r, "↩️ Batalkan semua"))
	if !strings.HasPrefix(undo.Text, "↩️ 7 catatan dibatalkan") {
		t.Fatalf("undo %q", undo.Text)
	}
	if after := entriesOn(t, db, "2026-11-18"); strings.Join(after, "|") != strings.Join(before, "|") {
		t.Fatalf("undo must remove only that paste: %q", after)
	}
	if again := cb(t, s, button(t, r, "↩️ Batalkan semua")); !strings.Contains(again.Text, "sudah tidak ada") {
		t.Fatalf("second undo %q", again.Text)
	}
	// every callback_data stays small
	for _, row := range r.Rows {
		for _, b := range row {
			if len(b.Data) > 64 {
				t.Fatalf("callback_data %q is %d bytes", b.Data, len(b.Data))
			}
		}
	}
}

// A paste with date markers, or more than 10 entries, is shown first.
func TestLogPreviewThenSimpan(t *testing.T) {
	s, db, _ := newService(t)
	paste := "2026-11-16\n- rapat senin\n- review\n2026-11-17\n- deploy"
	r := cmd(t, s, "log", paste)
	if !strings.Contains(r.Text, "Pratinjau: 3 catatan: Sen 16 Nov (2), Sel 17 Nov (1)") {
		t.Fatalf("preview %q", r.Text)
	}
	if len(entriesOn(t, db, "2026-11-16"))+len(entriesOn(t, db, "2026-11-17")) != 0 {
		t.Fatal("nothing is saved before Simpan")
	}
	saved := cb(t, s, button(t, r, "✅ Simpan"))
	if saved.Text != "✅ 3 catatan tersimpan" {
		t.Fatalf("after Simpan %q", saved.Text)
	}
	if strings.Join(entriesOn(t, db, "2026-11-16"), "|") != "rapat senin|review" || strings.Join(entriesOn(t, db, "2026-11-17"), "|") != "deploy" {
		t.Fatal("entries went to their own days")
	}
	if again := cb(t, s, button(t, r, "✅ Simpan")); !strings.Contains(again.Text, "kedaluwarsa atau sudah dipakai") {
		t.Fatalf("the preview is single-use: %q", again.Text)
	}

	// Batal keeps nothing; the preview is gone
	r = cmd(t, s, "log", "kemarin\n- a\n- b")
	if no := cb(t, s, button(t, r, "Batal")); no.Text != "Oke, tidak disimpan." {
		t.Fatalf("Batal %q", no.Text)
	}
	if got := entriesOn(t, db, "2026-11-17"); len(got) != 1 {
		t.Fatalf("Batal saved something: %q", got)
	}
	// more than 10 entries without any marker also waits
	var lines []string
	for i := range 11 {
		lines = append(lines, fmt.Sprintf("tugas %d", i))
	}
	r = cmd(t, s, "log", strings.Join(lines, "\n"))
	if !strings.Contains(r.Text, "Pratinjau: 11 catatan: Rab 18 Nov (11)") || len(entriesOn(t, db, "2026-11-18")) != 0 {
		t.Fatalf("11 entries: %q", r.Text)
	}
	// exactly 10 do not
	if r := cmd(t, s, "log", strings.Join(lines[:10], "\n")); r.Text != "✅ 10 catatan tersimpan" {
		t.Fatalf("10 entries: %q", r.Text)
	}
	// an expired preview is refused
	r = cmd(t, s, "log", "2026-11-16\n- x\n- y")
	s.Now = func() time.Time { return time.Date(2026, 11, 18, 10, 0, 0, 0, jkt) }
	if late := cb(t, s, button(t, r, "✅ Simpan")); !strings.Contains(late.Text, "kedaluwarsa") {
		t.Fatalf("expired preview: %q", late.Text)
	}
}

// Over the limits or with a future date: refused whole, nothing saved.
func TestLogRejectsWithoutSavingPartly(t *testing.T) {
	s, db, _ := newService(t)
	over := strings.TrimSuffix(strings.Repeat("baris\n", 51), "\n")
	long := "bagus\n" + strings.Repeat("x", MaxTextLen+1)
	for name, in := range map[string]string{"51 entries": over, "2001 characters": long, "future date": "ok\n2026-11-19\nbesok"} {
		r := cmd(t, s, "log", in)
		if !strings.HasPrefix(r.Text, "❌ ") {
			t.Errorf("%s: %q", name, r.Text)
		}
		if got := entriesOn(t, db, "2026-11-18"); len(got) != 0 {
			t.Fatalf("%s saved %d entries", name, len(got))
		}
	}
	if r := cmd(t, s, "log", over); !strings.Contains(r.Text, "maks 50") {
		t.Fatalf("limit message %q", r.Text)
	}
	if r := cmd(t, s, "log", long); !strings.Contains(r.Text, "baris 2 terlalu panjang") {
		t.Fatalf("line message %q", r.Text)
	}
}

// The text after "Tulis sekarang" / "Tambah" is split with the same rules,
// and the reminder closes; an edit is never split.
func TestPendingTextIsAPaste(t *testing.T) {
	g := newRig(t, "2026-11-18")
	ctx := context.Background()
	g.at("10:30")
	cb(t, g.s, button(t, g.last(), "✍️"))
	r, ok := g.s.PendingText(ctx, "- satu\n- dua\n- tiga")
	if !ok || r.Text != "✅ 3 catatan tersimpan" {
		t.Fatalf("paste after Tulis sekarang: %+v %v", r, ok)
	}
	if strings.Join(entriesOn(t, g.db, "2026-11-18"), "|") != "satu|dua|tiga" {
		t.Fatal("three entries")
	}
	if g.tg.edits[101] != "✅ Worklog sudah diisi." || g.events("worklog.closed") != 1 {
		t.Fatalf("the paste closes the reminder once: %v", g.tg.edits)
	}

	// Tambah on a past day: lines go to that day, markers still work
	cb(t, g.s, "add:2026-11-16")
	if r, _ := g.s.PendingText(ctx, "susulan a\nsusulan b"); r.Text != "✅ 2 catatan tersimpan (Senin 16 Nov 2026)" {
		t.Fatalf("add on a past day: %q", r.Text)
	}
	// an invalid paste keeps waiting
	cb(t, g.s, "add:2026-11-16")
	if r, _ := g.s.PendingText(ctx, "ok\n2030-01-01\nnanti"); !strings.Contains(r.Text, "Kirim lagi") {
		t.Fatalf("bad paste: %q", r.Text)
	}
	if r, ok := g.s.PendingText(ctx, "sekarang benar"); !ok || !strings.Contains(r.Text, "Tersimpan") {
		t.Fatalf("still waiting after a bad paste: %+v %v", r, ok)
	}
	// editing replaces one entry: line breaks become spaces
	es, _ := g.db.WorklogByDate(ctx, date("2026-11-18"))
	cb(t, g.s, fmt.Sprintf("edt:%d", es[0].ID))
	if r, _ := g.s.PendingText(ctx, "satu\ndiganti"); r.Text != "✏️ Diubah." {
		t.Fatalf("edit %q", r.Text)
	}
	if got := entriesOn(t, g.db, "2026-11-18"); got[0] != "satu diganti" || len(got) != 3 {
		t.Fatalf("edit must not split: %q", got)
	}
}

// Resending a paste from the offline queue (same client_id) never doubles it.
func TestAgentPasteIsIdempotent(t *testing.T) {
	s, db, _ := newService(t)
	ctx := context.Background()
	paste := "- a\n- b\n- c"
	r1, err := s.SaveFromAgent(ctx, "", paste, "paket-1", 0)
	if err != nil || r1.Entries != 3 || r1.Duplicate || r1.Count != 3 {
		t.Fatalf("first send %+v %v", r1, err)
	}
	for i := range 2 {
		r, err := s.SaveFromAgent(ctx, "", paste, "paket-1", 0)
		if err != nil || r.Entries != 0 || !r.Duplicate {
			t.Fatalf("resend %d: %+v %v", i+1, r, err)
		}
	}
	if got := entriesOn(t, db, "2026-11-18"); strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("rows after two resends: %q", got)
	}
	// a different paste with the same text is new (the owner pasted twice)
	if r, _ := s.SaveFromAgent(ctx, "", paste, "paket-2", 0); r.Entries != 3 {
		t.Fatalf("a new paste %+v", r)
	}
	// markers, default date and refusals work as in /log
	if r, err := s.SaveFromAgent(ctx, "2026-11-10", "x\nkemarin\ny", "paket-3", 0); err != nil || r.Entries != 2 {
		t.Fatalf("dated paste %+v %v", r, err)
	}
	if got := entriesOn(t, db, "2026-11-10"); strings.Join(got, "|") != "x" || strings.Join(entriesOn(t, db, "2026-11-17"), "|") != "y" {
		t.Fatalf("x on the default day, y on yesterday: %q", got)
	}
	for _, bad := range []string{"", "ok\n2030-01-01\nx"} {
		var ie *InputError
		if _, err := s.SaveFromAgent(ctx, "", bad, "paket-9", 0); err == nil || !asInput(err, &ie) {
			t.Errorf("%q should be an input error: %v", bad, err)
		}
	}
}

func asInput(err error, target **InputError) bool {
	ie, ok := err.(*InputError)
	*target = ie
	return ok
}

// --- Lewati / Senyapkan hari ini (PRD 6.5, 8a) -------------------------------

func TestLewatiSkipsOnlyThatSlot(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:30")
	if len(g.last().Rows[0]) != 2 || !strings.Contains(fmt.Sprint(g.last().Rows), "Lewati") || strings.Contains(fmt.Sprint(g.last().Rows), "Nanti") {
		t.Fatalf("two buttons, Tulis and Lewati: %+v", g.last().Rows)
	}
	r := cb(t, g.s, button(t, g.last(), "⏭ Lewati"))
	if r.Text != "⏭ Dilewati, aku ingatkan lagi di slot berikutnya." {
		t.Fatalf("Lewati %q", r.Text)
	}
	for _, c := range []string{"11:00", "12:00", "13:29"} {
		g.at(c)
	}
	if g.sent() != 1 {
		t.Fatal("no extra reminder between slots")
	}
	g.at("13:30")
	if g.sent() != 2 {
		t.Fatal("Lewati at 10:30 still lets the 13:30 reminder come")
	}
	ps, _ := g.db.PromptsOn(context.Background(), date("2026-11-18"))
	if ps[0].Status != "skipped" {
		t.Fatalf("slot status %q", ps[0].Status)
	}
	// the old "Nanti" button of messages sent before 8a means the same
	cb(t, g.s, "wln:2")
	g.at("16:30")
	if g.sent() != 3 {
		t.Fatal("the third slot")
	}
}

func TestClosingReminderAndSenyapkan(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:30")
	cb(t, g.s, button(t, g.last(), "⏭ Lewati"))
	g.at("13:30")
	cb(t, g.s, button(t, g.last(), "⏭ Lewati"))
	g.at("16:30")
	if r := cb(t, g.s, button(t, g.last(), "⏭ Lewati")); !strings.Contains(r.Text, "Pengingat penutup jam 17:30") {
		t.Fatalf("Lewati on the last slot announces the closing reminder: %q", r.Text)
	}
	g.at("17:29")
	if g.sent() != 3 {
		t.Fatal("not before 17:30")
	}
	g.at("17:30")
	if g.sent() != 4 {
		t.Fatal("closing reminder")
	}
	closing := g.last()
	if len(closing.Rows[0]) != 2 || !strings.Contains(fmt.Sprint(closing.Rows), "Senyapkan hari ini") || strings.Contains(fmt.Sprint(closing.Rows), "Lewati") {
		t.Fatalf("closing: Tulis sekarang and Senyapkan hari ini: %+v", closing.Rows)
	}
	r := cb(t, g.s, button(t, closing, "😴 Senyapkan hari ini"))
	if r.Text != "😴 Pengingat worklog disenyapkan untuk hari ini." {
		t.Fatalf("Senyapkan %q", r.Text)
	}
	g.at("17:45")
	g.at("18:30")
	if g.sent() != 4 {
		t.Fatal("silenced: nothing more today")
	}
	var muted string
	_, _ = g.db.GetSetting(context.Background(), KeyMuted, &muted)
	if muted != "2026-11-18" {
		t.Fatalf("worklog.muted_date = %q", muted)
	}
}

func TestSenyapOnlyForToday(t *testing.T) {
	g := newRig(t, "2026-11-18")
	ctx := context.Background()
	g.at("09:00")
	if r := g.r.Command(ctx, "senyap"); !strings.Contains(r.Text, "disenyapkan untuk hari ini") {
		t.Fatalf("/worklog senyap %q", r.Text)
	}
	if st := g.r.Command(ctx, ""); !strings.Contains(st.Text, "disenyapkan untuk hari ini") {
		t.Fatalf("status %q", st.Text)
	}
	g.at("10:30")
	g.at("13:30")
	if g.sent() != 0 {
		t.Fatal("muted day")
	}
	// the next working day reminds again
	n := *g.now
	*g.now = time.Date(n.Year(), n.Month(), 19, 10, 30, 0, 0, jkt)
	if err := g.r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if g.sent() != 1 {
		t.Fatal("tomorrow is not muted")
	}
	// and it can be undone the same day
	*g.now = time.Date(n.Year(), n.Month(), 19, 11, 0, 0, 0, jkt)
	g.r.Command(ctx, "senyap")
	if r := g.r.Command(ctx, "senyap hapus"); !strings.Contains(r.Text, "dinyalakan lagi") {
		t.Fatalf("senyap hapus %q", r.Text)
	}
	if st := g.r.Command(ctx, ""); strings.Contains(st.Text, "disenyapkan") {
		t.Fatal("unmuted")
	}
}

func TestAnswersFromTheAgent(t *testing.T) {
	g := newRig(t, "2026-11-18")
	ctx := context.Background()
	g.at("10:30")
	ps, _ := g.db.PromptsOn(ctx, date("2026-11-18"))
	id := ps[0].ID
	if _, err := g.r.Answer(ctx, id, "skip", "agent"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.tg.edits[101], "Dilewati, aku ingatkan lagi") || g.events("worklog.closed") != 1 {
		t.Fatalf("the Telegram message follows the agent: %v", g.tg.edits)
	}
	if _, err := g.r.Answer(ctx, id, "skip", "agent"); err != ErrPromptClosed {
		t.Fatalf("answering twice: %v", err)
	}
	g.at("13:30")
	ps, _ = g.db.PromptsOn(ctx, date("2026-11-18"))
	// "later" from an older agent is Lewati
	if _, err := g.r.Answer(ctx, ps[1].ID, "later", "agent"); err != nil {
		t.Fatalf("later: %v", err)
	}
	g.at("16:30")
	ps, _ = g.db.PromptsOn(ctx, date("2026-11-18"))
	if _, err := g.r.Answer(ctx, ps[2].ID, "mute", "agent"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.tg.edits[103], "disenyapkan") {
		t.Fatalf("mute edits the open message: %v", g.tg.edits)
	}
	g.at("17:30")
	if g.sent() != 3 {
		t.Fatal("muted from the agent: no closing reminder")
	}
	if _, err := g.r.Answer(ctx, ps[2].ID, "hapus", "agent"); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestWriteNowStillClosesThePrompt(t *testing.T) {
	g := newRig(t, "2026-11-18")
	g.at("10:30")
	ask := cb(t, g.s, button(t, g.last(), "✍️"))
	if !strings.Contains(ask.Text, "Satu baris = satu catatan") {
		t.Fatalf("write prompt %q", ask.Text)
	}
	r, ok := g.s.PendingText(context.Background(), "mengerjakan fitur worklog")
	if !ok || r.Text != "✅ Tersimpan (1 catatan hari ini)" {
		t.Fatalf("saved %+v", r)
	}
	if g.tg.edits[101] != "✅ Worklog sudah diisi." || g.events("worklog.closed") != 1 {
		t.Fatalf("prompt closed: %v", g.tg.edits)
	}
	if r := cb(t, g.s, "wls:1"); !strings.Contains(r.Text, "sudah selesai") {
		t.Fatalf("closed prompt buttons: %q", r.Text)
	}
}
