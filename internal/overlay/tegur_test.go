package overlay

import (
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/forms"
)

var tNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

const nudgeText = "Udah 30 menit di youtube.com nih. Lagi belajar atau nonton?"

func TestNudgeShowsAndNeedsAClickForKeys(t *testing.T) {
	p := &Presenter{}
	if !p.TegurDue(nudgeText, tNow) || p.State != TegurAsk || !p.Visible() {
		t.Fatalf("not shown: state %v", p.State)
	}
	if got := p.BubbleText(); !strings.HasPrefix(got, nudgeText+"\n") || !strings.Contains(got, HintTegurIdle) {
		t.Fatalf("bubble %q", got)
	}
	if p.KeysWanted() {
		t.Fatal("the nudge must not take the keyboard before it is clicked")
	}
	if p.TegurKey('l', tNow) != TegurNone || p.State != TegurAsk {
		t.Fatal("keys must be ignored before the click")
	}
	if l := p.Look(tNow); l.Form != forms.Tegur {
		t.Fatalf("form %v", l.Form)
	}
	p.OnHotkey(tNow) // Alt+Space focuses it
	if !p.KeysWanted() || !strings.Contains(p.BubbleText(), HintTegurKeys) {
		t.Fatalf("after focus: keys %v, bubble %q", p.KeysWanted(), p.BubbleText())
	}
	if !p.FocusTegur(tNow) {
		t.Fatal("click")
	}
}

func TestNudgeAnswers(t *testing.T) {
	for r, want := range map[rune]TegurAnswer{
		'l': TegurWorking, 'L': TegurWorking, '1': TegurWorking, '\r': TegurWorking,
		'o': TegurStop, 'O': TegurStop, '2': TegurStop, 0x1B: TegurStop,
	} {
		p := &Presenter{}
		p.TegurDue(nudgeText, tNow)
		p.FocusTegur(tNow)
		if got := p.TegurKey(r, tNow); got != want || p.State != Idle {
			t.Errorf("key %q = %v (state %v), want %v and a closed bubble", r, got, p.State, want)
		}
	}
	p := &Presenter{}
	p.TegurDue(nudgeText, tNow)
	p.FocusTegur(tNow)
	if got := p.TegurKey('x', tNow); got != TegurNone || p.State != TegurAsk {
		t.Fatalf("another key: %v state %v", got, p.State)
	}
}

func TestNudgeGoesAwayByItself(t *testing.T) {
	p := &Presenter{}
	p.TegurDue(nudgeText, tNow)
	p.Tick(tNow.Add(TegurAskFor - time.Second))
	if p.State != TegurAsk {
		t.Fatal("closed too early")
	}
	p.Tick(tNow.Add(TegurAskFor))
	if p.State != Idle {
		t.Fatalf("an unanswered nudge stays: state %v", p.State)
	}
	// once focused it follows the normal bubble idle time instead
	q := &Presenter{IdleClose: 20 * time.Second}
	q.TegurDue(nudgeText, tNow)
	q.FocusTegur(tNow)
	q.Tick(tNow.Add(19 * time.Second))
	if q.State != TegurAsk {
		t.Fatal("focused nudge closed early")
	}
	q.Tick(tNow.Add(20 * time.Second))
	if q.State != Idle {
		t.Fatal("focused nudge never closes")
	}
}

// Only chatter is replaced; anything the owner is in the middle of wins.
func TestNudgeDoesNotInterrupt(t *testing.T) {
	for _, st := range []State{Input, Thinking, Reply, Confirm, WorklogAsk, WorklogInput, TegurAsk} {
		p := &Presenter{State: st}
		if p.TegurDue(nudgeText, tNow) {
			t.Errorf("state %v was replaced by a nudge", st)
		}
	}
	p := &Presenter{}
	p.OnForm(forms.Output{Form: forms.Fokus, Speech: "semangat", Duration: 5 * time.Second}, tNow)
	if p.State != Showing || !p.TegurDue(nudgeText, tNow) || p.State != TegurAsk {
		t.Fatalf("chatter should give way: state %v", p.State)
	}
}

func TestNudgeTextsHaveGlyphs(t *testing.T) {
	face, err := NewFace(testFontPx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{nudgeText, HintTegurIdle, HintTegurKeys, "Udah 45 menit di Discord nih. Lagi belajar atau nonton?"} {
		for _, r := range s {
			if _, ok := face.GlyphAdvance(r); !ok {
				t.Errorf("font has no glyph for %q (U+%04X) in %q", r, r, s)
			}
		}
	}
}
