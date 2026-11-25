package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/forms"
)

func TestWorklogBubble(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{Form: forms.Biasa}
	if !p.WorklogDue(12, clk.now) || p.State != WorklogAsk {
		t.Fatal("worklog.due shows the ask bubble")
	}
	if p.KeysWanted() || p.Typing() {
		t.Fatal("the ask bubble must not take focus")
	}
	if !strings.HasSuffix(p.BubbleText(), HintWorklogIdle) || p.Look(clk.now).Form != forms.Ingat {
		t.Fatalf("unfocused text %q", p.BubbleText())
	}
	if a, _ := p.WorklogKey('n', clk.now); a != WorklogNone || p.State != WorklogAsk {
		t.Fatal("keys only after a click / Alt+Space")
	}
	p.OnHotkey(clk.now) // Alt+Space focuses it
	if !p.KeysWanted() || !strings.HasSuffix(p.BubbleText(), HintWorklogKeys) {
		t.Fatal("focused: Enter/N/Esc")
	}
	if a, id := p.WorklogKey('N', clk.now); a != WorklogSkip || id != 12 || p.State != Idle {
		t.Fatalf("N = Lewati this slot: %v %d %s", a, id, p.State)
	}

	p.WorklogDue(13, clk.now)
	p.FocusWorklog(clk.now) // a click
	if a, id := p.WorklogKey(0x1B, clk.now); a != WorklogSkip || id != 13 {
		t.Fatal("Esc = Lewati this slot (not the whole day)")
	}
	p.WorklogDue(15, clk.now)
	p.FocusWorklog(clk.now)
	if a, id := p.WorklogKey('s', clk.now); a != WorklogMute || id != 15 || p.State != Idle {
		t.Fatalf("S = Senyapkan hari ini: %v %d %s", a, id, p.State)
	}

	// Enter → write → saved ✓ with a hop
	p.WorklogDue(14, clk.now)
	p.FocusWorklog(clk.now)
	p.WorklogKey('\r', clk.now)
	if p.State != WorklogInput || !p.Typing() || p.BubbleText() != TextWorklogPrompt+"\n› "+Cursor {
		t.Fatalf("input: %s %q", p.State, p.BubbleText())
	}
	if p.Submit(clk.now) != "" {
		t.Fatal("the worklog box never goes to the chat")
	}
	p.Type([]rune("review PR tim"), clk.now)
	text, id := p.SubmitWorklog(clk.now)
	if text != "review PR tim" || id != 14 || p.State != WorklogSaving {
		t.Fatalf("submit %q %d %s", text, id, p.State)
	}
	p.WorklogSaved(true, "", clk.add(time.Second))
	if p.BubbleText() != TextWorklogSaved || p.Look(clk.now).Hop <= 0 && p.Look(clk.now.Add(HopFor/2)).Hop <= 0 {
		t.Fatalf("saved: %q", p.BubbleText())
	}
	p.Tick(clk.add(worklogSavedShowFor))
	if p.State != Idle {
		t.Fatal("the confirmation closes by itself")
	}

	// offline: kept locally
	p.OpenWorklog(0, clk.now) // tray "Tulis worklog…"
	p.Type([]rune("offline"), clk.now)
	p.SubmitWorklog(clk.now)
	p.WorklogSaved(false, "", clk.now)
	if p.BubbleText() != TextWorklogQueued {
		t.Fatalf("queued: %q", p.BubbleText())
	}
	// rejected
	p.Dismiss(clk.now)
	p.OpenWorklog(0, clk.now)
	p.Type([]rune("x"), clk.now)
	p.SubmitWorklog(clk.now)
	p.WorklogSaved(false, "catatan terlalu panjang", clk.now)
	if p.State != Failed || !strings.Contains(p.BubbleText(), "catatan terlalu panjang") {
		t.Fatalf("rejected: %s %q", p.State, p.BubbleText())
	}
}

func TestWorklogBubbleClosedAndBusy(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{}
	p.WorklogDue(20, clk.now)
	p.WorklogClosed(99, clk.now)
	if p.State != WorklogAsk {
		t.Fatal("another prompt's close does nothing")
	}
	p.WorklogClosed(20, clk.now) // answered in Telegram
	if p.State != Idle {
		t.Fatal("answered elsewhere: the bubble closes by itself")
	}
	// typing in the box is not interrupted by a close
	p.WorklogDue(21, clk.now)
	p.FocusWorklog(clk.now)
	p.WorklogKey('\r', clk.now)
	p.Type([]rune("lagi nulis"), clk.now)
	p.WorklogClosed(21, clk.now)
	if p.State != WorklogInput || p.Draft != "lagi nulis" {
		t.Fatal("a draft survives the close")
	}
	// busy (chatting): the reminder stays in Telegram only
	q := &Presenter{}
	q.OnHotkey(clk.now)
	if q.WorklogDue(22, clk.now) || q.State != Input {
		t.Fatal("never interrupts the owner")
	}
	// unanswered: hides after a while, focused: after IdleClose
	r := &Presenter{}
	r.WorklogDue(23, clk.now)
	r.Tick(clk.now.Add(WorklogAskFor))
	if r.State != Idle {
		t.Fatal("an unanswered worklog bubble hides")
	}
}

func TestLogCommandInChat(t *testing.T) {
	for in, want := range map[string]bool{"/log rapat": true, "/LOG rapat": true, " /log  a": true, "/log": false, "/login": false, "/logx a": false, "log a": false} {
		if IsLogCommand(in) != want {
			t.Errorf("IsLogCommand(%q) = %v", in, !want)
		}
	}
	p := &Presenter{}
	p.OnHotkey(t0)
	p.Type([]rune("/log deploy ke staging"), t0)
	text, id := p.SubmitWorklog(t0)
	if text != "deploy ke staging" || id != 0 || p.State != WorklogSaving {
		t.Fatalf("/log in the bubble: %q %d %s", text, id, p.State)
	}
	q := &Presenter{}
	q.OnHotkey(t0)
	q.Type([]rune("halo"), t0)
	if text, _ := q.SubmitWorklog(t0); text != "" || q.State != Input {
		t.Fatal("normal chat is not a worklog")
	}
}

func TestAskLocal(t *testing.T) {
	p := &Presenter{}
	if !p.AskLocal("Mau aku jalan otomatis saat laptop nyala?", "local:autostart", t0) || p.State != Confirm || !p.KeysWanted() {
		t.Fatal("local question")
	}
	p.Note("Oke.", t0)
	if p.State != Reply || p.BubbleText() != "Oke." {
		t.Fatal("note")
	}
	if p.AskLocal("x", "local:x", t0) {
		t.Fatal("never over another bubble")
	}
}

func TestTrayMenu(t *testing.T) {
	items := TrayMenu(MenuInfo{Status: "Tunas · online · sinkron 12 d lalu", Queued: 2, AutostartAvail: true,
		AutostartOn: true, AutostartStale: true, Paused: true, WorklogBubbles: true, CharacterShown: true, HasLog: true})
	var texts []string
	byID := map[int]MenuItem{}
	for _, it := range items {
		texts = append(texts, it.Text)
		byID[it.ID] = it
	}
	all := strings.Join(texts, "|")
	for _, want := range []string{"Tunas · online · sinkron 12 d lalu", "2 catatan worklog menunggu sinkron", "Jalankan saat Windows nyala",
		"Perbarui path autostart", MenuPause, "Pengingat worklog di layar", "Tulis worklog…", "Sembunyikan karakter",
		MenuLog, "Buka folder konfigurasi", MenuQuit} {
		if !strings.Contains(all, want) {
			t.Errorf("menu lacks %q", want)
		}
	}
	if !items[0].Grayed || !byID[CmdAutostart].Checked || !byID[CmdPause].Checked || !byID[CmdConfigDir].Grayed {
		t.Fatal("status line is not clickable; checks follow the state")
	}
	plain := TrayMenu(MenuInfo{Status: "Tunas"})
	for _, it := range plain {
		if strings.Contains(it.Text, "menunggu sinkron") || it.ID == CmdAutostart || it.Text == "Sembunyikan karakter" {
			t.Fatalf("unexpected %q", it.Text)
		}
	}
	px := TrayIconColor(ColPaused, 32)
	if c := px[(16*32+16)*4:]; c[0] != ColPaused.B || c[2] != ColPaused.R {
		t.Fatal("grey icon while paused")
	}
}
