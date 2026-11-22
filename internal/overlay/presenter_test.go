package overlay

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/agent"
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
	case Showing, Reply:
		if txt == "" || strings.Contains(txt, "›") {
			t.Fatalf("%s but bubble %q", p.State, txt)
		}
	case Input:
		if !strings.HasPrefix(txt, "› ") {
			t.Fatalf("input but bubble %q", txt)
		}
	case Thinking:
		if txt != TextThinking {
			t.Fatalf("thinking but bubble %q", txt)
		}
	case Slow:
		if txt != TextSlow {
			t.Fatalf("slow but bubble %q", txt)
		}
	case Failed:
		if !strings.Contains(txt, "\n› ") {
			t.Fatalf("failed but bubble %q", txt)
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
	for i := 0; (p.State == Showing || p.State == Reply) && i < 20; i++ {
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
	if got := p.Submit(t0); got != "halo tuna" || p.State != Thinking {
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
	if p.State != Thinking || p.Queued() != 1 {
		t.Fatal("thinking bubble first, reminder still waiting")
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
		now = now.Add(InputTimeout + ThinkLimit)
		p.Tick(now)
	}
	if p.State != Idle || p.Queued() != 0 {
		t.Fatalf("did not settle: %v queued=%d", p.State, p.Queued())
	}
}

// fakeClock is a manual clock for the time thresholds.
type fakeClock struct{ now time.Time }

func (c *fakeClock) add(d time.Duration) time.Time { c.now = c.now.Add(d); return c.now }

// sendChat opens the box, types text and presses Enter.
func sendChat(t *testing.T, p *Presenter, text string, now time.Time) {
	t.Helper()
	p.OnHotkey(now)
	p.Type([]rune(text), now)
	if got := p.Submit(now); got != text {
		t.Fatalf("submit = %q", got)
	}
}

func TestThinkingBecomesSlowAt8s(t *testing.T) {
	clk := &fakeClock{now: t0}
	var p Presenter
	sendChat(t, &p, "halo", clk.now)
	if p.State != Thinking || p.BubbleText() != TextThinking || !p.Waiting() {
		t.Fatalf("after Enter: %s %q", p.State, p.BubbleText())
	}
	p.Tick(clk.add(SlowAfter - time.Millisecond))
	if p.State != Thinking {
		t.Fatal("slow too early")
	}
	p.Tick(clk.add(time.Millisecond))
	if p.State != Slow || p.BubbleText() != TextSlow {
		t.Fatalf("at 8s: %s %q", p.State, p.BubbleText())
	}
	if p.WaitingFor(clk.now) != SlowAfter {
		t.Fatal("waiting time")
	}
	p.OnReply("Halo!", clk.add(2*time.Second))
	if p.State != Reply || p.BubbleText() != "Halo!" || p.Waiting() {
		t.Fatalf("reply: %s %q", p.State, p.BubbleText())
	}
	p.Tick(clk.add(minReplyFor))
	if p.State != Idle {
		t.Fatal("reply should expire")
	}
}

func TestReplyBeforeSlow(t *testing.T) {
	clk := &fakeClock{now: t0}
	var p Presenter
	sendChat(t, &p, "cepat", clk.now)
	p.Tick(clk.add(3 * time.Second))
	p.OnReply("Siap", clk.now)
	p.Tick(clk.add(6 * time.Second)) // past the 8s mark: must not turn Slow
	if p.State != Reply {
		t.Fatalf("state = %s", p.State)
	}
}

func TestInputLockedWhileThinking(t *testing.T) {
	var p Presenter
	sendChat(t, &p, "sekali", t0)
	p.Type([]rune("lagi"), t0)
	p.Backspace(t0)
	p.OnHotkey(t0)
	p.Dismiss(t0)
	if got := p.Submit(t0); got != "" {
		t.Fatalf("second send while thinking: %q", got)
	}
	if p.State != Thinking || p.Draft != "" {
		t.Fatalf("input must stay locked: %s %q", p.State, p.Draft)
	}
	p.Tick(t0.Add(SlowAfter))
	p.OnHotkey(t0.Add(SlowAfter))
	if p.State != Slow || p.Submit(t0.Add(SlowAfter)) != "" {
		t.Fatal("still locked when slow")
	}
}

func TestChatFailureReopensInput(t *testing.T) {
	want := map[agent.ChatFail]string{
		agent.FailTimeout: "Server lama menjawab, coba lagi ya",
		agent.FailOffline: "Tidak bisa hubungi server",
		agent.FailCap:     "Jatah LLM hari ini habis",
	}
	for kind, msg := range want {
		if FailText(kind) != msg {
			t.Fatalf("FailText(%s) = %q", kind, FailText(kind))
		}
		clk := &fakeClock{now: t0}
		var p Presenter
		sendChat(t, &p, "tolong cek", clk.now)
		p.Tick(clk.add(9 * time.Second)) // slow, then fails
		p.OnChatFailed(FailText(kind), clk.now)
		checkOne(t, &p)
		if p.State != Failed || !strings.HasPrefix(p.BubbleText(), msg+"\n› tolong cek") || !p.Typing() {
			t.Fatalf("%s: %s %q", kind, p.State, p.BubbleText())
		}
		// Enter retries the same text
		if got := p.Submit(clk.add(time.Second)); got != "tolong cek" || p.State != Thinking {
			t.Fatalf("retry = %q %s", got, p.State)
		}
		// the 8s threshold restarts with the retry
		p.Tick(clk.add(SlowAfter - time.Second))
		if p.State != Thinking {
			t.Fatal("retry should restart the slow timer")
		}
		p.OnChatFailed(msg, clk.now)
		p.OnHotkey(clk.now) // hotkey closes the retry box
		if p.State != Idle {
			t.Fatalf("hotkey on failed: %s", p.State)
		}
	}
	if !strings.Contains(FailText(agent.FailLLM), "LLM") {
		t.Error("llm failure text")
	}
}

func TestFailureIgnoredWhenNotWaiting(t *testing.T) {
	var p Presenter
	p.OnChatFailed("x", t0)
	if p.State != Idle {
		t.Fatal("late failure must not pop up")
	}
	p.OnHotkey(t0)
	p.Type([]rune("abc"), t0)
	p.OnChatFailed("x", t0)
	if p.State != Input || p.Draft != "abc" {
		t.Fatal("late failure must not touch the text box")
	}
}

func TestThinkLimitSafetyNet(t *testing.T) {
	var p Presenter
	sendChat(t, &p, "halo", t0)
	p.Tick(t0.Add(ThinkLimit))
	if p.State != Failed || !strings.HasPrefix(p.BubbleText(), FailText(agent.FailTimeout)) {
		t.Fatalf("no result after ThinkLimit: %s %q", p.State, p.BubbleText())
	}
	p.Tick(t0.Add(ThinkLimit + InputTimeout))
	if p.State != Idle {
		t.Fatal("failed box should time out like input")
	}
}

func TestDotsAnimation(t *testing.T) {
	var p Presenter
	if p.Dots(t0) != 0 {
		t.Fatal("no dots when idle")
	}
	sendChat(t, &p, "x", t0)
	var got []int
	for i := range 5 {
		got = append(got, p.Dots(t0.Add(time.Duration(i)*dotStep)))
	}
	if fmt.Sprint(got) != "[1 2 3 1 2]" {
		t.Fatalf("dots = %v", got)
	}
	lines := []string{"Tunas lagi", "mikir..."}
	if d := WithDots(lines, 1); d[1] != "mikir." || lines[1] != "mikir..." || d[0] != "Tunas lagi" {
		t.Fatalf("WithDots = %v (input %v)", d, lines)
	}
	// the bubble is laid out from the full "..." text, so it never resizes
	sc := ComputeScene("bottom-right", p.BubbleText(), mono)
	for n := 1; n <= 3; n++ {
		for _, l := range WithDots(sc.Lines, n) {
			if mono(l) > sc.Bubble.W-2*BubblePad {
				t.Fatalf("%d dots overflow the bubble", n)
			}
		}
	}
}

// One bubble through a whole chat with notifications arriving meanwhile.
func TestSingleBubbleThroughChat(t *testing.T) {
	clk := &fakeClock{now: t0}
	var p Presenter
	p.OnForm(chatter("ayo"), clk.now)
	sendChat(t, &p, "halo", clk.add(time.Second))
	p.OnForm(reminder("minum"), clk.now)
	p.OnForm(chatter("fokus"), clk.now)
	checkOne(t, &p)
	if p.State != Thinking || p.Queued() != 1 {
		t.Fatalf("thinking keeps the bubble: %s queued=%d", p.State, p.Queued())
	}
	p.Tick(clk.add(SlowAfter))
	p.OnForm(reminder("rapat"), clk.now)
	if p.State != Slow || p.Queued() != 2 {
		t.Fatal("slow keeps the bubble")
	}
	p.OnChatFailed(FailText(agent.FailOffline), clk.now)
	p.OnForm(reminder("tidur"), clk.now)
	if p.State != Failed || p.Queued() != 3 {
		t.Fatal("failed keeps the bubble")
	}
	p.Submit(clk.now)
	p.OnReply("Halo juga", clk.add(time.Second))
	checkOne(t, &p)
	if got := strings.Join(drain(t, &p, clk.now), "|"); got != "Halo juga|minum|rapat|tidur" {
		t.Fatalf("order = %s", got)
	}
}
