package overlay

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/forms"
)

// State is the bubble manager's state. There is exactly one bubble on
// screen at most: whatever happens while it is up either replaces it, waits
// in the queue, or is dropped, never a second bubble.
type State int

const (
	Idle    State = iota // nothing on screen
	Showing              // a speech bubble is displayed until hideAt
	Input                // the text box (after Alt+Space) is displayed
)

func (s State) String() string {
	return [...]string{"idle", "showing", "input"}[s]
}

// kind says how important a bubble is, which decides what may replace it.
type kind int

const (
	kindChatter  kind = iota // form speech (fokus, tegur, senang, ...): replaceable, never queued
	kindReminder             // ingat: must be seen, so it waits its turn
	kindReply                // chat reply: must be seen
	kindThinking             // "..." while waiting for a reply; replaced by the reply
)

type bubble struct {
	text string
	kind kind
	d    time.Duration
}

// Presenter is the bubble manager. It is driven by form changes, the hotkey,
// typing and chat replies on the UI thread; the clock is passed in.
//
//	Idle    --speech-->  Showing
//	Idle    --hotkey-->  Input
//	Showing --timeout/click--> next queued bubble, else Idle
//	Showing --hotkey-->  Input (an interrupted reminder goes back to the queue)
//	Input   --enter-->   Showing("...") / Idle when empty
//	Input   --esc/hotkey/timeout--> next queued bubble, else Idle
//
// Reminders and replies that arrive while another important bubble or the
// text box is up are queued (at most MaxQueue) and shown one after another.
type Presenter struct {
	State   State
	Form    forms.Form
	Draft   string // text being typed (Input)
	cur     bubble
	hideAt  time.Time
	typedAt time.Time // last input activity
	queue   []bubble
}

// Default display durations and limits.
const (
	DefaultShow  = 6 * time.Second
	minReplyFor  = 8 * time.Second
	maxReplyFor  = 30 * time.Second
	thinkingFor  = 60 * time.Second
	MaxDraftLen  = 300
	MaxQueue     = 4
	InputTimeout = 60 * time.Second // input mode is cancelled after this long without typing
)

// Visible reports whether the window should be on screen.
func (p *Presenter) Visible() bool { return p.State != Idle }

// Typing reports whether the text box is open.
func (p *Presenter) Typing() bool { return p.State == Input }

// Queued returns the number of bubbles waiting.
func (p *Presenter) Queued() int { return len(p.queue) }

// OnForm reacts to a new state-machine output. The pose always follows the
// form; only forms with something to say produce a bubble.
func (p *Presenter) OnForm(out forms.Output, now time.Time) {
	p.Form = out.Form
	if out.Speech == "" {
		return
	}
	d := out.Duration
	if d <= 0 {
		d = DefaultShow
	}
	b := bubble{text: out.Speech, kind: kindChatter, d: d}
	if out.Form == forms.Ingat {
		b.kind = kindReminder
	}
	if out.Form == forms.Tidur && p.State == Idle {
		return // do not pop up just to say Zzz
	}
	p.offer(b, now)
}

// OnReply shows a chat reply for a duration based on its length.
func (p *Presenter) OnReply(text string, now time.Time) {
	d := max(minReplyFor, time.Duration(utf8.RuneCountInString(text))*60*time.Millisecond)
	p.offer(bubble{text: text, kind: kindReply, d: min(d, maxReplyFor)}, now)
}

// offer is the single entry point for new bubbles.
func (p *Presenter) offer(b bubble, now time.Time) {
	switch p.State {
	case Idle:
		p.show(b, now)
	case Showing:
		switch {
		case b.kind == kindReply && p.cur.kind == kindThinking,
			p.cur.kind == kindChatter:
			p.show(b, now) // replace in place
		case b.kind == kindChatter:
			// an important bubble is up; stale chatter is not worth queueing
		default:
			p.enqueue(b)
		}
	case Input:
		if b.kind != kindChatter {
			p.enqueue(b)
		}
	}
}

func (p *Presenter) enqueue(b bubble) {
	if p.State == Showing && p.cur.text == b.text {
		return
	}
	for _, q := range p.queue {
		if q.text == b.text {
			return
		}
	}
	if len(p.queue) >= MaxQueue {
		p.queue = p.queue[1:] // drop the oldest
	}
	p.queue = append(p.queue, b)
}

// OnHotkey opens the text box, or closes it when already open.
func (p *Presenter) OnHotkey(now time.Time) {
	switch p.State {
	case Input:
		p.CancelInput(now)
		return
	case Showing:
		if p.cur.kind == kindReminder || p.cur.kind == kindReply {
			// not seen in full yet: show it again after the chat
			left := max(p.hideAt.Sub(now), DefaultShow)
			b := p.cur
			b.d = min(left, b.d)
			p.queue = append([]bubble{b}, p.queue...)
			if len(p.queue) > MaxQueue {
				p.queue = p.queue[:MaxQueue]
			}
		}
	}
	p.State, p.Draft, p.cur, p.hideAt = Input, "", bubble{}, time.Time{}
	p.typedAt = now
}

// Type appends runes to the draft.
func (p *Presenter) Type(rs []rune, now time.Time) {
	if p.State != Input {
		return
	}
	p.typedAt = now
	for _, r := range rs {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if utf8.RuneCountInString(p.Draft) >= MaxDraftLen {
			return
		}
		p.Draft += string(r)
	}
}

// Backspace removes the last rune of the draft.
func (p *Presenter) Backspace(now time.Time) {
	if p.State != Input {
		return
	}
	p.typedAt = now
	if p.Draft != "" {
		rs := []rune(p.Draft)
		p.Draft = string(rs[:len(rs)-1])
	}
}

// Submit ends input mode and returns the text to send ("" if empty).
func (p *Presenter) Submit(now time.Time) string {
	if p.State != Input {
		return ""
	}
	text := strings.TrimSpace(p.Draft)
	p.Draft = ""
	if text == "" {
		p.next(now)
		return ""
	}
	p.show(bubble{text: "...", kind: kindThinking, d: thinkingFor}, now)
	return text
}

// CancelInput closes the text box.
func (p *Presenter) CancelInput(now time.Time) {
	if p.State != Input {
		return
	}
	p.Draft = ""
	p.next(now)
}

// Dismiss hides the current bubble (click on Tunas); queued ones follow.
func (p *Presenter) Dismiss(now time.Time) {
	if p.State == Showing {
		p.next(now)
	}
}

// Tick expires the bubble or an idle text box.
func (p *Presenter) Tick(now time.Time) {
	switch p.State {
	case Input:
		if now.Sub(p.typedAt) >= InputTimeout {
			p.CancelInput(now)
		}
	case Showing:
		if !now.Before(p.hideAt) {
			p.next(now)
		}
	}
}

// BubbleText is what the one bubble should display now ("" = no bubble).
func (p *Presenter) BubbleText() string {
	switch p.State {
	case Input:
		return "› " + p.Draft + "▏"
	case Showing:
		return p.cur.text
	}
	return ""
}

func (p *Presenter) show(b bubble, now time.Time) {
	p.State, p.cur, p.hideAt = Showing, b, now.Add(b.d)
}

// next shows the oldest queued bubble, or goes idle.
func (p *Presenter) next(now time.Time) {
	if len(p.queue) > 0 {
		b := p.queue[0]
		p.queue = p.queue[1:]
		p.show(b, now)
		return
	}
	p.State, p.cur, p.hideAt = Idle, bubble{}, time.Time{}
}
