package overlay

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPasteKeepsLinesInTheWorklogBox(t *testing.T) {
	p := &Presenter{}
	p.OpenWorklog(0, t0)
	p.Paste("- rapat sprint\r\n- review PR\r\n\r\ndeploy", t0)
	if p.Draft != "- rapat sprint\n- review PR\n\ndeploy" {
		t.Fatalf("draft %q", p.Draft)
	}
	if bt := p.BubbleText(); !strings.HasPrefix(bt, TextWorklogPrompt+"\n› - rapat sprint\n- review PR") {
		t.Fatalf("bubble %q", bt)
	}
	// control characters other than line breaks are dropped
	p.Paste("a\x00b\x07c\td", t0)
	if !strings.HasSuffix(p.Draft, "abc\td") && !strings.HasSuffix(p.Draft, "abcd") {
		t.Fatalf("control characters: %q", p.Draft)
	}
	text, _ := p.SubmitWorklog(t0)
	if !strings.HasPrefix(text, "- rapat sprint\n- review PR") || p.State != WorklogSaving {
		t.Fatalf("submit %q %s", text, p.State)
	}
}

func TestPasteInChatBoxIsOneLineUnlessLogCommand(t *testing.T) {
	p := &Presenter{}
	p.OnHotkey(t0)
	p.Paste("halo\ntunas\n  apa kabar", t0)
	if p.Draft != "halo tunas apa kabar" {
		t.Fatalf("a chat message stays one line: %q", p.Draft)
	}
	// "/log " then a paste: the lines are kept (one line = one entry)
	q := &Presenter{}
	q.OnHotkey(t0)
	q.Type([]rune("/log "), t0)
	q.Paste("- a\n- b", t0)
	if q.Draft != "/log - a\n- b" {
		t.Fatalf("log draft %q", q.Draft)
	}
	text, _ := q.SubmitWorklog(t0)
	if text != "- a\n- b" {
		t.Fatalf("submitted %q", text)
	}
	// a paste outside a text box does nothing
	r := &Presenter{}
	r.Paste("x", t0)
	if r.Draft != "" {
		t.Fatal("no box, no paste")
	}
}

func TestPasteLimits(t *testing.T) {
	p := &Presenter{}
	p.OpenWorklog(0, t0)
	p.Paste(strings.Repeat("baris\n", 3000), t0)
	if n := utf8.RuneCountInString(p.Draft); n != MaxPasteLen {
		t.Fatalf("worklog paste capped at %d, got %d", MaxPasteLen, n)
	}
	p.Type([]rune("lagi"), t0)
	if utf8.RuneCountInString(p.Draft) != MaxPasteLen {
		t.Fatal("typing past the cap")
	}
	q := &Presenter{}
	q.OnHotkey(t0)
	q.Paste(strings.Repeat("a", 1000), t0)
	if utf8.RuneCountInString(q.Draft) != MaxDraftLen {
		t.Fatalf("a chat message keeps its %d limit", MaxDraftLen)
	}
}

func TestIsLogCommandWithNewline(t *testing.T) {
	for in, want := range map[string]bool{"/log a": true, "/log\n- a": true, "/LOG  x": true, "/log": false, "/log ": false, "/login a": false} {
		if got := IsLogCommand(in); got != want {
			t.Errorf("IsLogCommand(%q) = %v", in, got)
		}
	}
}

func TestSavedTextCountsEntries(t *testing.T) {
	clk := &fakeClock{now: t0}
	for _, c := range []struct {
		n      int
		synced bool
		want   string
	}{
		{1, true, TextWorklogSaved},
		{1, false, TextWorklogQueued},
		{8, true, "8 catatan tersimpan √"},
		{8, false, "8 catatan tersimpan √ (menunggu sinkron)"},
	} {
		p := &Presenter{}
		p.BeginWorklogSave(c.n, clk.now)
		p.WorklogSaved(c.synced, "", clk.now)
		if p.BubbleText() != c.want {
			t.Errorf("n=%d synced=%v → %q, want %q", c.n, c.synced, p.BubbleText(), c.want)
		}
	}
	p := &Presenter{}
	p.BeginWorklogSave(3, clk.now)
	p.WorklogSaved(false, "terlalu panjang", clk.now)
	if p.State != Failed || !strings.Contains(p.BubbleText(), "worklog ditolak: terlalu panjang") {
		t.Fatalf("rejected %s %q", p.State, p.BubbleText())
	}
}

// The clipboard path asks with Enter/Esc (the same local question as autostart).
func TestClipboardPreviewQuestion(t *testing.T) {
	p := &Presenter{}
	if !p.AskLocal("Simpan 8 catatan: 24 Nov (5), 25 Nov (3)?", "local:clipboard", t0) || p.State != Confirm {
		t.Fatal("ask")
	}
	if bt := p.BubbleText(); !strings.HasPrefix(bt, "Simpan 8 catatan") || !strings.HasSuffix(bt, ConfirmHint) {
		t.Fatalf("bubble %q", bt)
	}
	if !p.KeysWanted() {
		t.Fatal("Enter/Esc must reach the window")
	}
	p.BeginWorklogSave(8, t0.Add(time.Second))
	if p.State != WorklogSaving || p.WorklogCount() != 8 {
		t.Fatalf("saving %s %d", p.State, p.WorklogCount())
	}
}

func TestTrayMenuHasTheNewWorklogItems(t *testing.T) {
	var ids []int
	var texts []string
	for _, it := range TrayMenu(MenuInfo{Status: "Tunas"}) {
		ids = append(ids, it.ID)
		texts = append(texts, it.Text)
	}
	all := strings.Join(texts, "|")
	if !strings.Contains(all, "Tulis worklog dari clipboard") || !strings.Contains(all, "Senyapkan worklog hari ini") {
		t.Fatalf("menu: %s", all)
	}
	seen := map[int]bool{}
	for _, id := range ids {
		if id != 0 && seen[id] {
			t.Fatalf("duplicate menu command id %d", id)
		}
		seen[id] = true
	}
}
