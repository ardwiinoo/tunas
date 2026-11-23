package overlay

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// The phase gate path: idle → input → mikir → lama → sangat lama →
// berhasil → input (same bubble) → mikir → gagal → input → tutup.
func TestBubbleFullCycle(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{IdleClose: 30 * time.Second}
	expect := func(want State) {
		t.Helper()
		checkOne(t, p)
		if p.State != want {
			t.Fatalf("at %s: %s, want %s", clk.now.Sub(t0), p.State, want)
		}
	}
	expect(Idle)
	p.OnHotkey(clk.now)
	expect(Input)
	p.Type([]rune("pertanyaan satu"), clk.now)
	p.Submit(clk.now)
	expect(Thinking)
	p.Tick(clk.add(DefaultSlowAfter))
	expect(Slow)
	p.Tick(clk.add(DefaultVerySlowAfter - DefaultSlowAfter))
	expect(VerySlow)
	p.OnReply("jawaban satu", clk.add(time.Second))
	expect(Reply)
	p.Tick(clk.add(5 * time.Second)) // typed out
	if !p.Conversing() || !p.KeysWanted() || !p.Typing() {
		t.Fatal("after the answer the text box must be open: the bubble is not locked")
	}
	// keep talking in the same bubble
	p.Type([]rune("pertanyaan dua"), clk.now)
	if got := p.Submit(clk.now); got != "pertanyaan dua" {
		t.Fatalf("second message = %q", got)
	}
	expect(Thinking)
	p.OnChatFailed(FailText(agent.FailDown), clk.add(time.Second))
	expect(Failed)
	if !p.Typing() || p.Draft != "pertanyaan dua" {
		t.Fatal("failure reopens the box with the text")
	}
	p.CancelInput(clk.now) // Esc
	expect(Idle)
}

func TestConversationContext(t *testing.T) {
	clk := &fakeClock{now: t0}
	p := &Presenter{}
	say := func(q, a string) {
		t.Helper()
		p.OnHotkey(clk.now)
		p.Type([]rune(q), clk.now)
		p.Submit(clk.now)
		p.OnReply(a, clk.add(time.Second))
		p.SkipReveal(clk.now)
	}
	say("halo", "hai!")
	if len(p.Context()) != 0 {
		t.Fatal("the first message has no context")
	}
	// same bubble: the previous turn travels with the next message
	p.Type([]rune("apa kabar?"), clk.now)
	p.Submit(clk.now)
	if got := fmt.Sprint(p.Context()); got != "[{user halo} {assistant hai!}]" {
		t.Fatalf("context = %s", got)
	}
	p.OnReply("baik!", clk.add(time.Second))
	p.SkipReveal(clk.now)

	// closed for less than ContextReset: the conversation continues
	p.CancelInput(clk.now)
	clk.add(ContextReset - time.Second)
	say("lagi", "oke")
	if len(p.Context()) != 4 {
		t.Fatalf("context after a short break = %v", p.Context())
	}
	// only the last MaxContext turns are kept
	for i := range 5 {
		p.Type([]rune(fmt.Sprint("q", i)), clk.now)
		p.Submit(clk.now)
		p.OnReply(fmt.Sprint("a", i), clk.add(time.Second))
		p.SkipReveal(clk.now)
	}
	p.Type([]rune("terakhir"), clk.now)
	p.Submit(clk.now)
	if ctx := p.Context(); len(ctx) != MaxContext || ctx[len(ctx)-1].Text != "a4" {
		t.Fatalf("context capped at %d: %v", MaxContext, ctx)
	}
	p.OnReply("ok", clk.add(time.Second))
	p.SkipReveal(clk.now)

	// closed for more than ContextReset: a new conversation
	p.CancelInput(clk.now)
	clk.add(ContextReset + time.Second)
	p.OnHotkey(clk.now)
	p.Type([]rune("baru"), clk.now)
	p.Submit(clk.now)
	if len(p.Context()) != 0 {
		t.Fatalf("context must reset after %s closed: %v", ContextReset, p.Context())
	}
}

func TestHotkeyFocusesOpenBubble(t *testing.T) {
	p := &Presenter{}
	sendChat(t, p, "halo", t0)
	p.OnReply("hai, ada yang bisa kubantu?", t0)
	p.OnHotkey(t0.Add(100 * time.Millisecond)) // while it is still typed out
	if p.State != Reply || !p.Conversing() {
		t.Fatalf("Alt+Space on an answer focuses its text box, got %s conversing=%v", p.State, p.Conversing())
	}
	p.Type([]rune("lanjut"), t0)
	p.OnHotkey(t0)
	if p.Draft != "lanjut" || p.State != Reply {
		t.Fatal("Alt+Space must not drop the draft")
	}
	// Enter on an empty box under an answer does nothing (it does not close)
	q := &Presenter{}
	sendChat(t, q, "x", t0)
	q.OnReply("y", t0)
	q.SkipReveal(t0)
	if q.Submit(t0) != "" || q.State != Reply {
		t.Fatal("empty Enter in a conversation keeps the bubble")
	}
}

func TestIdleCloseIsConfigurable(t *testing.T) {
	for _, d := range []time.Duration{15 * time.Second, 2 * time.Minute} {
		p := &Presenter{IdleClose: d}
		p.OnHotkey(t0)
		p.Tick(t0.Add(d - time.Millisecond))
		if p.State != Input {
			t.Fatalf("%s: closed early", d)
		}
		p.Tick(t0.Add(d))
		if p.State != Idle {
			t.Fatalf("%s: not closed", d)
		}
	}
}

func TestScrollLongText(t *testing.T) {
	p := &Presenter{}
	long := strings.Repeat("kata panjang sekali ", 40)
	sendChat(t, p, "x", t0)
	p.OnReply(long, t0)
	p.SkipReveal(t0)
	sc := ComputeScene("bottom-right", p.BubbleText(), mono)
	maxUp := len(sc.All) - MaxLines
	if maxUp <= 0 {
		t.Fatal("test text should be scrollable")
	}
	if p.ScrollUp(maxUp) != 0 {
		t.Fatal("starts at the bottom (the text box)")
	}
	p.ScrollBy(3)
	p.ScrollBy(1000)
	if p.ScrollUp(maxUp) != maxUp {
		t.Fatal("scroll is clamped at the top")
	}
	top := sc.View(sc.All, p.ScrollUp(maxUp))
	if top.Lines[0] != sc.All[0] || !top.MoreBelow || top.MoreAbove {
		t.Fatalf("top view %+v", top)
	}
	p.Type([]rune("a"), t0) // typing jumps back to the text box
	if p.ScrollUp(maxUp) != 0 {
		t.Fatal("typing must scroll back to the bottom")
	}
	p.ScrollBy(-5)
	if p.ScrollUp(maxUp) != 0 {
		t.Fatal("cannot scroll below the bottom")
	}
}
