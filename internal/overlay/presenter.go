package overlay

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// State is the bubble manager's state. There is exactly one bubble on
// screen at most: whatever happens while it is up either replaces it, waits
// in the queue, or is dropped, never a second bubble.
type State int

const (
	Idle     State = iota // nothing on screen
	Showing               // a notification bubble (reminder, form speech) until hideAt
	Input                 // the text box (after Alt+Space)
	Thinking              // message sent, waiting for the reply; input locked
	Slow                  // still waiting after SlowAfter; input locked
	Failed                // the chat failed: reason plus the text box to retry
	Reply                 // the chat reply until hideAt
)

func (s State) String() string {
	return [...]string{"idle", "showing", "input", "thinking", "slow", "failed", "reply"}[s]
}

// kind says how important a bubble is, which decides what may replace it.
type kind int

const (
	kindChatter  kind = iota // form speech (fokus, tegur, senang, ...): replaceable, never queued
	kindReminder             // ingat: must be seen, so it waits its turn
	kindReply                // chat reply: must be seen
)

type bubble struct {
	text string
	kind kind
	d    time.Duration
}

// Presenter is the bubble manager. It is driven by form changes, the hotkey,
// typing and chat results on the UI thread; the clock is passed in, so the
// whole machine is testable with a fake clock.
//
//	Idle     --speech-->  Showing          Idle/Showing --hotkey--> Input
//	Input    --enter-->   Thinking         Input --esc/hotkey/timeout--> next
//	Thinking --8s-->      Slow             Thinking/Slow --reply--> Reply
//	Thinking/Slow --error--> Failed (reason + previous text, Enter retries)
//	Thinking/Slow --ThinkLimit--> Failed (safety net if no result arrives)
//	Showing/Reply --timeout/click--> next queued bubble, else Idle
//
// While thinking the input is locked: typing, Enter and the hotkey are
// ignored, so a message cannot be sent twice. Reminders and replies that
// arrive while another important bubble is up are queued (at most MaxQueue)
// and shown one after another; form chatter is replaced in place or dropped.
type Presenter struct {
	State   State
	Form    forms.Form
	Draft   string // text being typed (Input, Failed)
	cur     bubble // Showing, Reply
	failMsg string // Failed
	sent    string // last submitted text, restored on failure
	hideAt  time.Time
	typedAt time.Time // last input activity
	since   time.Time // when Thinking started
	queue   []bubble
}

// Timings and limits.
const (
	DefaultShow  = 6 * time.Second
	minReplyFor  = 8 * time.Second
	maxReplyFor  = 30 * time.Second
	SlowAfter    = 8 * time.Second  // Thinking becomes Slow
	ThinkLimit   = 40 * time.Second // give up waiting (the chat call itself times out sooner)
	dotStep      = 400 * time.Millisecond
	MaxDraftLen  = 300
	MaxQueue     = 4
	InputTimeout = 60 * time.Second // the text box closes after this long without typing
)

// Bubble texts. Every character must exist in the embedded font (see
// TestFixedTextsHaveGlyphs); U+258F, for example, does not.
const (
	Cursor       = "|"
	TextThinking = "Tunas lagi mikir..."
	TextSlow     = "Agak lama, sebentar ya..."
)

// FailText is the bubble text for a failed chat.
func FailText(f agent.ChatFail) string {
	switch f {
	case agent.FailTimeout:
		return "Server lama menjawab, coba lagi ya"
	case agent.FailOffline:
		return "Tidak bisa hubungi server"
	case agent.FailCap:
		return "Jatah LLM hari ini habis"
	case agent.FailLLM:
		return "LLM sedang bermasalah, coba lagi"
	case agent.FailBlocked:
		return "Model AI diblokir gateway (cek konfigurasi)"
	case agent.FailQuota:
		return "Kuota provider AI habis, coba nanti"
	case agent.FailDown:
		return "Provider AI sedang down, coba lagi"
	case agent.FailDisabled:
		return "AI belum diaktifkan di server"
	case agent.FailAuth:
		return "Perangkat ditolak server (cek device_token)"
	case agent.FailBusy:
		return "Terlalu sering, tunggu sebentar ya"
	}
	return "Server sedang bermasalah, coba lagi"
}

// Visible reports whether the window should be on screen.
func (p *Presenter) Visible() bool { return p.State != Idle }

// Typing reports whether keystrokes go to the text box.
func (p *Presenter) Typing() bool { return p.State == Input || p.State == Failed }

// Waiting reports whether a chat reply is pending (input locked).
func (p *Presenter) Waiting() bool { return p.State == Thinking || p.State == Slow }

// WaitingFor is how long the current chat has been pending.
func (p *Presenter) WaitingFor(now time.Time) time.Duration {
	if !p.Waiting() {
		return 0
	}
	return now.Sub(p.since)
}

// Dots is the number of animated dots (1..3) while waiting, else 0.
func (p *Presenter) Dots(now time.Time) int {
	if !p.Waiting() {
		return 0
	}
	return 1 + int(now.Sub(p.since)/dotStep)%3
}

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

// OnReply delivers a chat reply.
func (p *Presenter) OnReply(text string, now time.Time) {
	d := max(minReplyFor, time.Duration(utf8.RuneCountInString(text))*60*time.Millisecond)
	b := bubble{text: text, kind: kindReply, d: min(d, maxReplyFor)}
	if p.Waiting() {
		p.show(b, now)
		return
	}
	p.offer(b, now) // a late reply: treat like any other important bubble
}

// OnChatFailed reports a failed chat with a user-facing reason. The text box
// reopens with the message that was sent, so Enter retries it.
func (p *Presenter) OnChatFailed(reason string, now time.Time) {
	if !p.Waiting() {
		return // nobody is waiting for it any more
	}
	p.fail(reason, now)
}

func (p *Presenter) fail(reason string, now time.Time) {
	p.State, p.failMsg, p.Draft = Failed, reason, p.sent
	p.cur, p.hideAt, p.typedAt = bubble{}, time.Time{}, now
}

// offer routes a new notification or late reply.
func (p *Presenter) offer(b bubble, now time.Time) {
	switch p.State {
	case Idle:
		p.show(b, now)
	case Showing:
		switch {
		case p.cur.kind == kindChatter:
			p.show(b, now) // replace in place
		case b.kind != kindChatter:
			p.enqueue(b)
		}
	default: // Input, Thinking, Slow, Failed, Reply
		if b.kind != kindChatter {
			p.enqueue(b)
		}
	}
}

func (p *Presenter) enqueue(b bubble) {
	if (p.State == Showing || p.State == Reply) && p.cur.text == b.text {
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

// OnHotkey opens the text box, or closes it when already open. Ignored
// while a reply is pending.
func (p *Presenter) OnHotkey(now time.Time) {
	switch p.State {
	case Input, Failed:
		p.CancelInput(now)
		return
	case Thinking, Slow:
		return
	case Showing, Reply:
		if p.cur.kind == kindReminder || p.cur.kind == kindReply {
			// not seen in full yet: show it again after the chat
			b := p.cur
			b.d = min(max(p.hideAt.Sub(now), DefaultShow), b.d)
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
	if !p.Typing() {
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
	if !p.Typing() {
		return
	}
	p.typedAt = now
	if p.Draft != "" {
		rs := []rune(p.Draft)
		p.Draft = string(rs[:len(rs)-1])
	}
}

// Submit sends the draft: it returns the text to send ("" if there is
// nothing to send, including while a reply is still pending).
func (p *Presenter) Submit(now time.Time) string {
	if !p.Typing() {
		return ""
	}
	text := strings.TrimSpace(p.Draft)
	p.Draft = ""
	if text == "" {
		p.next(now)
		return ""
	}
	p.State, p.sent, p.since, p.failMsg = Thinking, text, now, ""
	p.cur, p.hideAt = bubble{}, time.Time{}
	return text
}

// CancelInput closes the text box.
func (p *Presenter) CancelInput(now time.Time) {
	if !p.Typing() {
		return
	}
	p.Draft, p.failMsg = "", ""
	p.next(now)
}

// Dismiss hides the current bubble (click on Tunas); queued ones follow.
func (p *Presenter) Dismiss(now time.Time) {
	if p.State == Showing || p.State == Reply {
		p.next(now)
	}
}

// Tick advances time-based transitions.
func (p *Presenter) Tick(now time.Time) {
	switch p.State {
	case Input, Failed:
		if now.Sub(p.typedAt) >= InputTimeout {
			p.CancelInput(now)
		}
	case Thinking, Slow:
		w := now.Sub(p.since)
		if w >= ThinkLimit {
			p.fail(FailText(agent.FailTimeout), now)
		} else if w >= SlowAfter {
			p.State = Slow
		}
	case Showing, Reply:
		if !now.Before(p.hideAt) {
			p.next(now)
		}
	}
}

// BubbleText is the text the one bubble lays out ("" = no bubble). While
// waiting it ends in "..."; the renderer animates the dots (see WithDots)
// so the bubble keeps its size.
func (p *Presenter) BubbleText() string {
	switch p.State {
	case Input:
		return "› " + p.Draft + Cursor
	case Failed:
		return p.failMsg + "\n› " + p.Draft + Cursor
	case Thinking:
		return TextThinking
	case Slow:
		return TextSlow
	case Showing, Reply:
		return p.cur.text
	}
	return ""
}

// WithDots returns lines with the trailing dots of the last line set to n
// (1..3). n == 0 returns lines unchanged.
func WithDots(lines []string, n int) []string {
	if n <= 0 || len(lines) == 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	last := strings.TrimRight(out[len(out)-1], ".")
	out[len(out)-1] = last + strings.Repeat(".", n)
	return out
}

func (p *Presenter) show(b bubble, now time.Time) {
	p.State = Showing
	if b.kind == kindReply {
		p.State = Reply
	}
	p.cur, p.hideAt = b, now.Add(b.d)
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
