package overlay

import (
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/forms"
)

var t0 = time.Unix(1000, 0)

func reminder(text string) forms.Output {
	return forms.Output{Form: forms.Ingat, Speech: text, Duration: 15 * time.Second}
}

func chatter(text string) forms.Output {
	return forms.Output{Form: forms.Tegur, Speech: text, Duration: 5 * time.Second}
}

// checkOne asserts the single-bubble invariant: the state alone decides what
// the one bubble shows.
func checkOne(t *testing.T, p *Presenter) {
	t.Helper()
	txt := p.BubbleText()
	switch p.State {
	case Idle:
		if txt != "" || p.Visible() {
			t.Fatalf("idle but bubble %q", txt)
		}
	case Showing:
		if txt == "" || strings.HasPrefix(txt, "›") {
			t.Fatalf("showing but bubble %q", txt)
		}
	case Input:
		if !strings.HasPrefix(txt, "› ") {
			t.Fatalf("input but bubble %q", txt)
		}
	}
	if p.Queued() > MaxQueue {
		t.Fatalf("queue %d > %d", p.Queued(), MaxQueue)
	}
}

// drain clicks through every bubble and returns their texts in order.
func drain(t *testing.T, p *Presenter, now time.Time) []string {
	t.Helper()
	var seen []string
	for i := 0; p.State == Showing && i < 20; i++ {
		seen = append(seen, p.BubbleText())
		p.Dismiss(now)
		checkOne(t, p)
	}
	return seen
}

func TestPresenterBasics(t *testing.T) {
	var p Presenter
	p.OnForm(forms.Output{Form: forms.Fokus}, t0)
	if p.State != Idle || p.Form != forms.Fokus {
		t.Fatal("quiet form must only change the pose")
	}
	p.OnForm(forms.Output{Form: forms.Tidur, Speech: "Zzz..."}, t0)
	if p.State != Idle {
		t.Fatal("tidur must not pop up")
	}
	p.OnForm(reminder("minum"), t0)
	if p.State != Showing || p.BubbleText() != "minum" {
		t.Fatalf("ingat = %+v", p)
	}
	p.Tick(t0.Add(14 * time.Second))
	if p.State != Showing {
		t.Fatal("hid too early")
	}
	p.Tick(t0.Add(15 * time.Second))
	if p.State != Idle {
		t.Fatal("should hide after duration")
	}
}

func TestPresenterTyping(t *testing.T) {
	var p Presenter
	p.OnHotkey(t0)
	p.Type([]rune("halo\x08 tunas"), t0)
	p.Backspace(t0)
	if p.State != Input || p.Draft != "halo tuna" || !strings.HasPrefix(p.BubbleText(), "› halo tuna") {
		t.Fatalf("draft = %q", p.Draft)
	}
	p.Tick(t0.Add(InputTimeout - time.Second))
	if p.State != Input {
		t.Fatal("input closed within the timeout")
	}
	if got := p.Submit(t0); got != "halo tuna" || p.BubbleText() != "..." {
		t.Fatalf("submit = %q %+v", got, p)
	}
	p.OnReply("Halo juga!", t0)
	p.Tick(t0.Add(7 * time.Second))
	if p.BubbleText() != "Halo juga!" {
		t.Fatal("reply must replace the thinking bubble and stay a while")
	}
	p.Dismiss(t0)
	if p.State != Idle {
		t.Fatal("dismiss")
	}

	p.OnHotkey(t0)
	if p.Submit(t0) != "" || p.State != Idle {
		t.Fatal("empty submit should close")
	}
	p.OnHotkey(t0)
	p.OnHotkey(t0)
	if p.State != Idle {
		t.Fatal("second hotkey closes the text box")
	}

	p.OnHotkey(t0)
	p.Type([]rune(strings.Repeat("a", MaxDraftLen+50)), t0)
	if utf8.RuneCountInString(p.Draft) != MaxDraftLen {
		t.Fatal("draft not capped")
	}
	p.Type([]rune("b"), t0.Add(30*time.Second)) // activity extends the timeout
	p.Tick(t0.Add(80 * time.Second))
	if p.State != Input {
		t.Fatal("typing should extend the input timeout")
	}
	p.Tick(t0.Add(91 * time.Second))
	if p.State != Idle {
		t.Fatal("idle input should be cancelled after InputTimeout")
	}
}

// The reported bug: a reminder is up and Alt+Space is pressed.
func TestHotkeyDuringReminder(t *testing.T) {
	var p Presenter
	p.OnForm(reminder("minum obat"), t0)
	p.OnHotkey(t0.Add(2 * time.Second))
	checkOne(t, &p)
	if p.State != Input || strings.Contains(p.BubbleText(), "minum") {
		t.Fatalf("text box must replace the reminder bubble, got %q", p.BubbleText())
	}
	if p.Queued() != 1 {
		t.Fatal("interrupted reminder must wait in the queue")
	}
	p.CancelInput(t0.Add(3 * time.Second))
	if p.BubbleText() != "minum obat" {
		t.Fatalf("reminder should come back after the chat, got %q", p.BubbleText())
	}
	// remaining time was 13s; it is shown again for that long
	p.Tick(t0.Add(15 * time.Second))
	if p.State != Showing {
		t.Fatal("re-shown reminder hid too early")
	}
	p.Tick(t0.Add(16 * time.Second))
	if p.State != Idle {
		t.Fatal("re-shown reminder should expire")
	}
}

func TestReminderDuringInputWaits(t *testing.T) {
	var p Presenter
	p.OnHotkey(t0)
	p.Type([]rune("hai"), t0)
	p.OnForm(reminder("rapat"), t0)
	p.OnForm(chatter("ayo fokus"), t0)
	checkOne(t, &p)
	if p.State != Input || p.Draft != "hai" || p.Queued() != 1 {
		t.Fatalf("typing must not be interrupted; queued=%d", p.Queued())
	}
	p.Submit(t0)
	if p.BubbleText() != "..." {
		t.Fatal("thinking bubble first")
	}
	p.OnReply("Hai!", t0)
	if got := drain(t, &p, t0); strings.Join(got, "|") != "Hai!|rapat" {
		t.Fatalf("order = %v", got)
	}
}

func TestBubblesReplaceOrQueue(t *testing.T) {
	var p Presenter
	p.OnForm(chatter("a"), t0)
	p.OnForm(chatter("b"), t0)
	if p.BubbleText() != "b" || p.Queued() != 0 {
		t.Fatal("chatter replaces chatter in place")
	}
	p.OnForm(reminder("r1"), t0)
	if p.BubbleText() != "r1" || p.Queued() != 0 {
		t.Fatal("reminder replaces chatter in place")
	}
	p.OnForm(chatter("c"), t0)
	if p.BubbleText() != "r1" || p.Queued() != 0 {
		t.Fatal("chatter must not cover or queue behind a reminder")
	}
	p.OnForm(reminder("r2"), t0)
	p.OnReply("balasan", t0)
	p.OnForm(reminder("r2"), t0) // duplicate
	p.OnForm(reminder("r1"), t0) // same as the current one
	if p.BubbleText() != "r1" || p.Queued() != 2 {
		t.Fatalf("queue = %d", p.Queued())
	}
	if got := drain(t, &p, t0); strings.Join(got, "|") != "r1|r2|balasan" {
		t.Fatalf("order = %v", got)
	}

	for i := range MaxQueue + 3 {
		p.OnForm(reminder(string(rune('A'+i))), t0)
	}
	if p.Queued() != MaxQueue {
		t.Fatalf("queue not capped: %d", p.Queued())
	}
	got := drain(t, &p, t0)
	if len(got) != MaxQueue+1 || got[0] != "A" || got[len(got)-1] != string(rune('A'+MaxQueue+2)) {
		t.Fatalf("capped queue keeps the current and the newest: %v", got)
	}
}

func TestReplyAfterHotkeyDuringThinking(t *testing.T) {
	var p Presenter
	p.OnHotkey(t0)
	p.Type([]rune("tanya"), t0)
	p.Submit(t0)
	p.OnHotkey(t0) // open the box again while waiting
	if p.Queued() != 0 {
		t.Fatal("the thinking bubble is not worth re-showing")
	}
	p.OnReply("jawaban", t0)
	checkOne(t, &p)
	if p.State != Input || p.Queued() != 1 {
		t.Fatal("reply must wait while typing")
	}
	p.OnHotkey(t0)
	if p.BubbleText() != "jawaban" {
		t.Fatalf("reply after closing the box, got %q", p.BubbleText())
	}
}

// Triggers that land in the same tick, in every order, must never produce
// more than one bubble or lose the reminder or the reply.
func TestSimultaneousTriggers(t *testing.T) {
	type ev func(p *Presenter, now time.Time)
	evs := map[string]ev{
		"hotkey":   func(p *Presenter, now time.Time) { p.OnHotkey(now) },
		"reminder": func(p *Presenter, now time.Time) { p.OnForm(reminder("R"), now) },
		"reply":    func(p *Presenter, now time.Time) { p.OnReply("J", now) },
		"chatter":  func(p *Presenter, now time.Time) { p.OnForm(chatter("C"), now) },
	}
	names := []string{"hotkey", "reminder", "reply", "chatter"}
	var perms [][]string
	var permute func([]string, int)
	permute = func(a []string, k int) {
		if k == len(a) {
			perms = append(perms, append([]string(nil), a...))
			return
		}
		for i := k; i < len(a); i++ {
			a[k], a[i] = a[i], a[k]
			permute(a, k+1)
			a[k], a[i] = a[i], a[k]
		}
	}
	permute(names, 0)

	for _, start := range []string{"idle", "showing", "input"} {
		for _, perm := range perms {
			var p Presenter
			switch start {
			case "showing":
				p.OnForm(chatter("awal"), t0)
			case "input":
				p.OnHotkey(t0)
			}
			for _, n := range perm {
				evs[n](&p, t0)
				checkOne(t, &p)
			}
			if p.State == Input {
				p.CancelInput(t0)
			}
			got := strings.Join(drain(t, &p, t0), "|")
			if !strings.Contains(got, "R") || !strings.Contains(got, "J") || p.State != Idle {
				t.Errorf("start=%s order=%v: shown %q", start, perm, got)
			}
		}
	}
}

// Random event sequences keep the invariant and always end idle.
func TestPresenterRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var p Presenter
	now := t0
	for range 5000 {
		now = now.Add(time.Duration(rng.Intn(3000)) * time.Millisecond)
		switch rng.Intn(9) {
		case 0:
			p.OnHotkey(now)
		case 1:
			p.OnForm(reminder(string(rune('a'+rng.Intn(6)))), now)
		case 2:
			p.OnForm(chatter("c"), now)
		case 3:
			p.OnReply("j", now)
		case 4:
			p.Type([]rune("x"), now)
		case 5:
			p.Submit(now)
		case 6:
			p.Dismiss(now)
		case 7:
			p.OnForm(forms.Output{Form: forms.Tidur, Speech: "Zzz"}, now)
		default:
			p.Tick(now)
		}
		checkOne(t, &p)
	}
	for range 2*MaxQueue + 2 {
		now = now.Add(InputTimeout + thinkingFor)
		p.Tick(now)
	}
	if p.State != Idle || p.Queued() != 0 {
		t.Fatalf("did not settle: %v queued=%d", p.State, p.Queued())
	}
}
