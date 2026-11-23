package worklog

import (
	"context"
	"strings"
	"testing"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/llm"
)

type countLLM struct{ calls int }

func (c *countLLM) Complete(_ context.Context, msgs []llm.Message) (string, error) {
	c.calls++
	return "• poin dari " + strings.SplitN(msgs[1].Content, "\n", 2)[0], nil
}

func TestRekap(t *testing.T) {
	s, _, _ := newService(t) // today: Wed 18 Nov 2026
	for _, a := range []string{"2026-11-02 rapat", "2026-11-03 tes", "2026-11-03 deploy", "2026-11-16 review"} {
		cmd(t, s, "log", a)
	}
	r := cmd(t, s, "rekap", "")
	// working days before today: 2-6, 9-13, 16, 17 = 12; filled 2, 3, 16
	if !strings.Contains(r.Text, "Hari kerja: 12 · terisi: 3 · terlewat: 9") || !strings.Contains(r.Text, "Terlewat: 4, 5, 6, 9, 10, 11, 12, 13, 17 Nov") {
		t.Fatalf("summary:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "Selasa 3 Nov 2026\n• tes\n• deploy") {
		t.Fatalf("days:\n%s", r.Text)
	}
	// a past month counts every working day
	if r := cmd(t, s, "rekap", "2026-10"); !strings.Contains(r.Text, "Hari kerja: 22 · terisi: 0") {
		t.Fatalf("october: %s", r.Text)
	}
	if r := cmd(t, s, "rekap", "kemarin"); !strings.Contains(r.Text, "Pakai") {
		t.Fatal("usage")
	}

	// long → a document
	var docName string
	s.SendDoc = func(_ context.Context, name string, data []byte, _ string) error {
		docName = name
		if len(data) <= TelegramLimit {
			t.Error("document should hold the long recap")
		}
		return nil
	}
	for i := range 40 {
		cmd(t, s, "log", "2026-11-17 "+strings.Repeat("kata ", 20)+string(rune('a'+i%26)))
	}
	if r := cmd(t, s, "rekap", ""); docName != "rekap-2026-11.md" || !strings.Contains(r.Text, "dokumen") {
		t.Fatalf("long recap: %q doc %q", r.Text, docName)
	}

	// calendar button: the recap goes below the calendar, which stays
	var sent []bot.Reply
	s.Send = func(_ context.Context, r bot.Reply) error { sent = append(sent, r); return nil }
	if r := cb(t, s, "rek:2026-10"); !r.Keep || len(sent) != 1 {
		t.Fatalf("calendar recap: %+v sent %d", r, len(sent))
	}

	// /rekap ai: warns once, then runs; the model gets the recap text
	ai := &countLLM{}
	s.LLM = ai
	warn := cmd(t, s, "rekap", "2026-10 ai")
	if !strings.Contains(warn.Text, "penyedia model") || ai.calls != 0 {
		t.Fatal("ask before sending worklog text to the provider")
	}
	if r := cb(t, s, button(t, warn, "✅")); !strings.Contains(r.Text, "🤖 Rekap AI Oktober 2026") || ai.calls != 1 {
		t.Fatalf("after consent: %q", r.Text)
	}
	if r := cmd(t, s, "rekap", "2026-10 ai"); !strings.Contains(r.Text, "🤖") || ai.calls != 2 {
		t.Fatal("consent is asked only once")
	}
	if r := cb(t, s, "rxn"); !strings.Contains(r.Text, "dibatalkan") {
		t.Fatal("cancel")
	}
}
