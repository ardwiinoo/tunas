package assist

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/forms"
	"github.com/ardwiinoo/tunas/internal/llm"
)

// phraser answers the classifier with a fixed intent and, when asked to
// phrase FAKTA, calls answer with the facts it was given.
type phraser struct {
	intent string
	answer func(facts string) string
	seen   []string
}

func (p *phraser) Complete(_ context.Context, msgs []llm.Message) (string, error) {
	user := msgs[len(msgs)-1].Content
	p.seen = append(p.seen, msgs[0].Content)
	if _, facts, ok := strings.Cut(user, "FAKTA:\n"); ok {
		return p.answer(facts), nil
	}
	return p.intent, nil
}

var reDuration = regexp.MustCompile(`Code\.exe — [^\n]+`)

// (a) The bubble's data answer carries the same numbers as /today.
func TestBubbleDataQuestionMatchesToday(t *testing.T) {
	llmFake := &phraser{
		intent: `{"intent":"question","template":"top_apps","range":"today"}`,
		answer: func(facts string) string { return "Hari ini kamu paling lama di " + reDuration.FindString(facts) + "." },
	}
	a, db, now := setup(t, llmFake)
	seedActivity(t, db, time.Date(2026, 11, 23, 8, 0, 0, 0, jkt))

	r, err := a.Handle(context.Background(), forms.Biasa, "hari ini aku paling lama pakai apa?", nil)
	if err != nil || ConfirmID(r) != "" {
		t.Fatalf("reply %+v err %v", r, err)
	}
	cmds := &bot.Commands{DB: db, Loc: jkt, Now: func() time.Time { return *now }, Log: slog.New(slog.DiscardHandler)}
	today := cmds.Handle(context.Background(), "/today")

	want := reDuration.FindString(today) // e.g. "Code.exe — 1 j 30 m"
	if want == "" || !strings.Contains(r.Text, want) {
		t.Fatalf("bubble %q does not match /today line %q\n/today:\n%s", r.Text, want, today)
	}
	if len(llmFake.seen) != 2 {
		t.Fatalf("expected classify + phrase, got %d LLM calls", len(llmFake.seen))
	}
}

// (b) A model that invents numbers does not get through: the reply falls
// back to the query result.
func TestBubbleInventedNumbersRejected(t *testing.T) {
	llmFake := &phraser{
		intent: `{"intent":"question","template":"total_active","range":"today"}`,
		answer: func(string) string { return "Hari ini kamu aktif 7 jam 45 menit, mantap!" },
	}
	a, db, _ := setup(t, llmFake)
	seedActivity(t, db, time.Date(2026, 11, 23, 8, 0, 0, 0, jkt))

	r, err := a.Handle(context.Background(), forms.Biasa, "hari ini berapa lama aku aktif?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Text, "7 jam 45") {
		t.Fatalf("invented numbers passed: %q", r.Text)
	}
	if !strings.Contains(r.Text, "Total waktu aktif: 1 j 50 m") {
		t.Fatalf("should fall back to the query result, got %q", r.Text)
	}

	// numbers that are in the facts pass untouched
	llmFake.answer = func(string) string { return "Total aktifmu hari ini 1 j 50 m." }
	if r, _ := a.Handle(context.Background(), forms.Biasa, "berapa lama?", nil); r.Text != "Total aktifmu hari ini 1 j 50 m." {
		t.Fatalf("faithful answer replaced: %q", r.Text)
	}
}

func TestForeignNumbers(t *testing.T) {
	facts := "Rentang: hari ini (Sen 23 Nov s.d. Sen 23 Nov)\nSesi terpanjang: Code.exe selama 1 j 30 m, Sen 23 Nov 08:05–09:35"
	if bad := foreignNumbers("Sesi terpanjangmu 1 j 30 m di Code.exe, mulai jam 8:05.", facts); len(bad) != 0 {
		t.Fatalf("faithful answer flagged: %v", bad)
	}
	if bad := foreignNumbers("Kamu fokus 2 jam, 95% produktif!", facts); len(bad) != 2 {
		t.Fatalf("invented numbers not flagged: %v", bad)
	}
}

// (c) "ingatkan aku ..." from the bubble proposes first and only writes a
// reminders row after the owner confirms.
func TestBubbleReminderCreatesRowAfterConfirm(t *testing.T) {
	f := &fakeLLM{replies: []string{
		`{"intent":"reminder","when":"17:00","text":"minum obat"}`,
		`{"intent":"task","title":"kirim laporan"}`,
	}}
	a, db, _ := setup(t, f)
	ctx := context.Background()

	r, err := a.Handle(ctx, forms.Fokus, "ingatkan aku jam 5 sore minum obat", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := ConfirmID(r)
	if id == "" || !strings.Contains(r.Text, "17:00") || !strings.Contains(r.Text, "minum obat") {
		t.Fatalf("proposal = %+v", r)
	}
	if rs, _ := db.PendingReminders(ctx, 10); len(rs) != 0 {
		t.Fatal("nothing may be saved before confirmation")
	}
	got := a.Confirm(ctx, id, true)
	rs, err := db.PendingReminders(ctx, 10)
	if err != nil || len(rs) != 1 || rs[0].Text != "minum obat" {
		t.Fatalf("reminders = %+v %v", rs, err)
	}
	if !strings.Contains(got, "Oke, diingatkan") || !strings.Contains(got, "minum obat") {
		t.Fatalf("confirm reply = %q", got)
	}
	if again := a.Confirm(ctx, id, true); !strings.Contains(again, "kedaluwarsa") {
		t.Fatalf("an id is single-use: %q", again)
	}

	// Esc = Batal: nothing saved
	r, _ = a.Handle(ctx, forms.Biasa, "catat tugas kirim laporan", nil)
	if got := a.Confirm(ctx, ConfirmID(r), false); got != "Oke, dibatalkan." {
		t.Fatalf("cancel = %q", got)
	}
	if ts, _ := db.OpenTasks(ctx, 10); len(ts) != 0 {
		t.Fatal("cancelled task was saved")
	}
}

// The chat path carries the guardrail and no personal data.
func TestChatGuardrail(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"intent":"chat"}`, "Halo!"}}
	a, _, _ := setup(t, f)
	if _, err := a.Handle(context.Background(), forms.Biasa, "halo", nil); err != nil {
		t.Fatal(err)
	}
	sys := f.seen[1][0].Content
	for _, want := range []string{"TIDAK punya akses", "jangan pernah mengarang", "jangan pernah bilang sudah melakukannya"} {
		if !strings.Contains(sys, want) {
			t.Errorf("chat system prompt lacks %q", want)
		}
	}
}

// The bubble's recent turns reach the chat call, between the system
// prompt and the new message.
func TestChatUsesHistory(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"intent":"chat"}`, "Masih ingat kok!"}}
	a, _, _ := setup(t, f)
	hist := []llm.Message{{Role: "user", Content: "namaku Budi"}, {Role: "assistant", Content: "Halo Budi!"}}
	r, err := a.Handle(context.Background(), forms.Biasa, "siapa namaku?", hist)
	if err != nil || r.Text != "Masih ingat kok!" {
		t.Fatalf("reply %+v %v", r, err)
	}
	msgs := f.seen[1]
	if len(msgs) != 4 || msgs[1].Content != "namaku Budi" || msgs[2].Role != "assistant" || msgs[3].Content != "siapa namaku?" {
		t.Fatalf("chat messages = %+v", msgs)
	}
}
