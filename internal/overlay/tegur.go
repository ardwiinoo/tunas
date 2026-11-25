package overlay

import "time"

// The distraction nudge bubble (PRD Fase 8c):
//
//	nudge → TegurAsk (no focus taken; the form is "tegur")
//	click / Alt+Space on it → keys: L or Enter = "Lagi belajar/kerja", O or Esc = "Oke, aku stop"
//
// Like the worklog reminder, keys only work once the bubble is clicked or
// Alt+Space is pressed (a window without focus gets no keys). Unanswered, it
// goes away by itself and counts as "Oke, aku stop".

// Nudge texts.
const (
	HintTegurIdle = "klik atau Alt+Space untuk menjawab"
	HintTegurKeys = "L = lagi belajar/kerja · O = oke, aku stop"
	TegurAskFor   = 2 * time.Minute
)

// TegurAnswer is what a key on the nudge means.
type TegurAnswer int

// Answers.
const (
	TegurNone    TegurAnswer = iota
	TegurWorking             // "Lagi belajar/kerja": quiet about this app or site for a while
	TegurStop                // "Oke, aku stop": closed; asks again only after the cooldown
)

// TegurDue shows the nudge without taking focus. It only replaces chatter or
// nothing; whatever else is going on wins (the answer would be lost on a busy
// bubble, and the next nudge comes after the cooldown anyway).
func (p *Presenter) TegurDue(text string, now time.Time) bool {
	if p.State != Idle && !(p.State == Showing && p.cur.kind == kindChatter) {
		return false
	}
	p.State, p.tegurText, p.tegurKeys, p.typedAt = TegurAsk, text, false, now
	p.cur, p.hideAt = bubble{}, time.Time{}
	return true
}

// FocusTegur is a click or Alt+Space on the nudge: the keys work now.
func (p *Presenter) FocusTegur(now time.Time) bool {
	if p.State != TegurAsk {
		return false
	}
	p.tegurKeys, p.typedAt = true, now
	return true
}

// TegurKey handles a key on the focused nudge and returns the answer.
func (p *Presenter) TegurKey(r rune, now time.Time) TegurAnswer {
	if p.State != TegurAsk || !p.tegurKeys {
		return TegurNone
	}
	switch r {
	case 'l', 'L', '1', '\r':
		p.next(now)
		return TegurWorking
	case 'o', 'O', '2', 0x1B:
		p.next(now)
		return TegurStop
	}
	return TegurNone
}

func (p *Presenter) tegurBubble() string {
	hint := HintTegurIdle
	if p.tegurKeys {
		hint = HintTegurKeys
	}
	return p.tegurText + "\n" + hint
}

func (p *Presenter) tegurTick(now time.Time) {
	limit := TegurAskFor
	if p.tegurKeys {
		limit = p.idleClose()
	}
	if now.Sub(p.typedAt) >= limit {
		p.next(now)
	}
}
