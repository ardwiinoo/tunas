package overlay

import (
	"fmt"
	"math"
	"math/rand/v2"
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
	Idle          State = iota // nothing on screen
	Showing                    // a notification bubble (reminder, form speech) until hideAt
	Input                      // the text box (after Alt+Space)
	Thinking                   // mikir: message sent, waiting; input locked
	Slow                       // mikir lama: still waiting after SlowAfter
	VerySlow                   // sangat lama: still waiting after VerySlowAfter; Esc cancels
	Failed                     // gagal: reason plus the text box to retry
	Reply                      // berhasil: small hop, then the answer typed out
	Confirm                    // a proposal (reminder/task): Enter = Ya, Esc = Batal
	WorklogAsk                 // a worklog reminder (no focus until clicked / Alt+Space)
	WorklogInput               // the worklog text box
	WorklogSaving              // sending the worklog entry
	TegurAsk                   // the distraction nudge: a question with two answers (no focus until clicked / Alt+Space)
)

func (s State) String() string {
	return [...]string{"idle", "tampil", "input", "mikir", "mikir_lama", "sangat_lama", "gagal", "berhasil", "konfirmasi",
		"worklog_tanya", "worklog_tulis", "worklog_simpan"}[s]
}

// kind says how important a bubble is, which decides what may replace it.
type kind int

const (
	kindChatter  kind = iota // form speech (fokus, tegur, senang, ...): replaceable, never queued
	kindReminder             // ingat: must be seen, so it waits its turn
	kindReply                // chat reply: must be seen
)

type bubble struct {
	text    string
	kind    kind
	d       time.Duration
	search  bool     // the answer came from a web search (bicara_cari)
	sources []string // site names shown after the text ("kompas.com")
}

// LoadingLines are the playful texts shown while waiting, one list per
// phase (modes/mikir.yaml, overridable without recompiling).
type LoadingLines struct {
	Mikir      []string
	MikirLama  []string
	SangatLama []string
}

// DefaultLoadingLines is used when no lines are configured.
var DefaultLoadingLines = LoadingLines{
	Mikir:      []string{"hmm...", "bentar ya...", "lagi mikir nih"},
	MikirLama:  []string{"agak lama ya...", "sabar ya, lagi nanya ke server"},
	SangatLama: []string{"maaf lama, masih nyari jawabannya..."},
}

// Presenter is the bubble manager. It is driven by form changes, the hotkey,
// keys and chat results on the UI thread; the clock is passed in, so the
// whole machine is testable with a fake clock.
//
//	Idle/Showing --hotkey--> Input --Enter--> Thinking (mikir)
//	Thinking --SlowAfter--> Slow (mikir lama) --VerySlowAfter--> VerySlow (sangat lama)
//	Thinking/Slow/VerySlow --reply--> Reply (hop, then typed out; Enter skips)
//	Thinking/Slow/VerySlow --error--> Failed (sad pose, reason, text box again)
//	Thinking/Slow/VerySlow --Esc--> next (request cancelled, result ignored)
//	Thinking/Slow/VerySlow --proposal--> Confirm --Enter--> Thinking --> Reply
//	Confirm --Esc/hotkey--> Reply("Oke, dibatalkan.")
//	Showing/Reply --timeout/click--> next queued bubble, else Idle
//
// While waiting the input is locked, so a message cannot be sent twice.
// Every request gets an id (Req); results for another id are stale.
type Presenter struct {
	State State
	Form  forms.Form
	Draft string // text being typed (Input, Failed)

	// Settings (zero values = defaults).
	SlowAfter     time.Duration // default 3 s
	VerySlowAfter time.Duration // default 8 s
	NoAnimations  bool          // static poses, no typing effect
	Lines         LoadingLines  // default DefaultLoadingLines
	Pick          func(n int) int
	IdleClose     time.Duration // close the open bubble after this long without activity (default 60 s)

	// The "bicara" gesture (config.yaml speech.*): an answer is typed out
	// over len × SpeechMsPerChar, kept between SpeechMin and SpeechMax.
	SpeechMsPerChar time.Duration // default 40 ms
	SpeechMin       time.Duration // default 2 s
	SpeechMax       time.Duration // default 8 s

	cur       bubble // Showing, Reply
	failMsg   string // Failed
	sent      string // last submitted text, restored on failure
	hideAt    time.Time
	typedAt   time.Time // last input activity
	since     time.Time // when the current wait started
	phaseLine string    // the text picked for the current waiting phase
	req       int       // id of the newest request
	shownAt   time.Time // Reply: when the answer arrived
	typed     bool      // Reply: a chat answer (hop + typing effect), not a plain bubble
	revealAll bool      // Reply: typing effect skipped or off
	failedAt  time.Time // Failed: when it failed (sad pose)
	queue     []bubble
	scrollUp  int // lines scrolled up from the bottom of a long bubble

	stopped bool // the owner ended the gesture (Enter, typing, a reminder); it does not come back

	history  []Turn    // the conversation so far (last MaxContext turns)
	asked    []Turn    // history sent with the pending request
	closedAt time.Time // when the bubble last closed (context resets after ContextReset)

	confirmID string // Confirm
	confirmQ  string // Confirm: the interpretation shown to the owner

	wlPrompt  int64     // WorklogAsk/WorklogInput: the reminder being answered (0 = none)
	wlCount   int       // entries in the worklog being saved
	wlFocused bool      // WorklogAsk: clicked or Alt+Space, the keys work
	tegurText string    // TegurAsk: the question
	tegurKeys bool      // TegurAsk: clicked or Alt+Space, the keys work
	hopAt     time.Time // a small hop after "Tersimpan ✓"
}

// Timings and limits.
const (
	DefaultShow          = 6 * time.Second
	minReplyFor          = 8 * time.Second
	maxReplyFor          = 30 * time.Second
	DefaultSlowAfter     = 3 * time.Second
	DefaultVerySlowAfter = 8 * time.Second
	ThinkLimit           = 40 * time.Second // give up waiting (the chat call itself times out sooner)
	HopFor               = 400 * time.Millisecond
	SadFor               = 1500 * time.Millisecond
	NodFor               = 250 * time.Millisecond // a small nod at the end of a sentence
	DefaultMsPerChar     = 40 * time.Millisecond
	DefaultSpeechMin     = 2 * time.Second
	DefaultSpeechMax     = 8 * time.Second
	speechClose          = 300 * time.Millisecond // the mouth closes smoothly at the end
	dotStep              = 400 * time.Millisecond
	MaxDraftLen          = 300
	MaxQueue             = 4
	InputTimeout         = 60 * time.Second // default IdleClose
	MaxContext           = 6                // turns of conversation sent with a message
	ContextReset         = 5 * time.Minute  // a bubble closed this long starts a new conversation
)

// Turn is one message of the bubble conversation.
type Turn = agent.ChatTurn

// Fixed bubble texts. Every character must exist in the embedded font (see
// TestFixedTextsHaveGlyphs); U+258F, for example, does not.
const (
	Cursor        = "|"
	ConfirmHint   = "Enter = Ya · Esc = Batal"
	CancelHint    = "Esc = batal"
	TextCancelled = "Oke, dibatalkan."
)

// FailText is the bubble text for a failed chat.
func FailText(f agent.ChatFail) string {
	switch f {
	case agent.FailTimeout:
		return "server lama menjawab, coba lagi?"
	case agent.FailOffline:
		return "tidak bisa hubungi server"
	case agent.FailCap:
		return "jatah LLM hari ini habis"
	case agent.FailLLM:
		return "LLM sedang bermasalah, coba lagi?"
	case agent.FailBlocked:
		return "model AI diblokir gateway (cek konfigurasi)"
	case agent.FailQuota:
		return "kuota provider AI habis, coba nanti"
	case agent.FailDown:
		return "provider AI sedang down, coba lagi?"
	case agent.FailDisabled:
		return "AI belum diaktifkan di server"
	case agent.FailAuth:
		return "perangkat ditolak server (cek device_token)"
	case agent.FailBusy:
		return "terlalu sering, tunggu sebentar ya"
	}
	return "server sedang bermasalah, coba lagi?"
}

// Visible reports whether the window should be on screen.
func (p *Presenter) Visible() bool { return p.State != Idle }

// Typing reports whether keystrokes go to the text box: the input, the
// retry box after a failure, and the reply (the conversation continues).
func (p *Presenter) Typing() bool {
	return p.State == Input || p.State == Failed || p.State == WorklogInput || (p.State == Reply && p.typed)
}

// Conversing reports whether a chat answer is up with the text box under it.
func (p *Presenter) Conversing() bool { return p.State == Reply && p.typed && p.revealAll }

func (p *Presenter) idleClose() time.Duration {
	if p.IdleClose > 0 {
		return p.IdleClose
	}
	return InputTimeout
}

// Context is the conversation history to send with the pending request.
func (p *Presenter) Context() []Turn { return append([]Turn(nil), p.asked...) }

func (p *Presenter) remember(t Turn) {
	p.history = append(p.history, t)
	if len(p.history) > MaxContext {
		p.history = p.history[len(p.history)-MaxContext:]
	}
}

// ScrollBy scrolls a long bubble: positive = up (towards the start).
func (p *Presenter) ScrollBy(lines int) { p.scrollUp = max(0, p.scrollUp+lines) }

// ScrollUp is how many lines the bubble is scrolled up from the bottom,
// clamped to maxUp (the renderer knows the line count).
func (p *Presenter) ScrollUp(maxUp int) int {
	p.scrollUp = min(p.scrollUp, max(0, maxUp))
	return p.scrollUp
}

// Waiting reports whether a chat result is pending (input locked).
func (p *Presenter) Waiting() bool {
	return p.State == Thinking || p.State == Slow || p.State == VerySlow
}

// KeysWanted reports whether the window needs keyboard focus: the text box,
// a proposal (Enter/Esc), a pending request (Esc cancels) or an answer
// still being typed out (Enter skips).
func (p *Presenter) KeysWanted() bool {
	return p.Typing() || p.State == Confirm || p.Waiting() || (p.State == WorklogAsk && p.wlFocused) || (p.State == TegurAsk && p.tegurKeys)
}

// Req is the id of the newest request; results for other ids are stale.
func (p *Presenter) Req() int { return p.req }

// WaitingFor is how long the current request has been pending.
func (p *Presenter) WaitingFor(now time.Time) time.Duration {
	if !p.Waiting() {
		return 0
	}
	return now.Sub(p.since)
}

// Dots is the number of animated dots (1..3) while waiting, else 0. Without
// animations it stays at 3.
func (p *Presenter) Dots(now time.Time) int {
	if !p.Waiting() {
		return 0
	}
	if p.NoAnimations {
		return 3
	}
	return 1 + int(now.Sub(p.since)/dotStep)%3
}

// Queued returns the number of bubbles waiting.
func (p *Presenter) Queued() int { return len(p.queue) }

func (p *Presenter) slowAfter() time.Duration {
	if p.SlowAfter > 0 {
		return p.SlowAfter
	}
	return DefaultSlowAfter
}

func (p *Presenter) verySlowAfter() time.Duration {
	if p.VerySlowAfter > p.slowAfter() {
		return p.VerySlowAfter
	}
	return max(DefaultVerySlowAfter, p.slowAfter()+time.Second)
}

// pickLine chooses a random line for a waiting phase.
func (p *Presenter) pickLine(lines, fallback []string) string {
	if len(lines) == 0 {
		lines = fallback
	}
	i := 0
	if len(lines) > 1 {
		if p.Pick != nil {
			i = p.Pick(len(lines)) % len(lines)
		} else {
			i = rand.IntN(len(lines))
		}
	}
	// always laid out with "..." so the animated dots never resize the bubble
	return strings.TrimRight(lines[i], ".…") + "..."
}

func (p *Presenter) enterWait(s State) {
	p.State = s
	switch s {
	case Thinking:
		p.phaseLine = p.pickLine(p.Lines.Mikir, DefaultLoadingLines.Mikir)
	case Slow:
		p.phaseLine = p.pickLine(p.Lines.MikirLama, DefaultLoadingLines.MikirLama)
	case VerySlow:
		p.phaseLine = p.pickLine(p.Lines.SangatLama, DefaultLoadingLines.SangatLama)
	}
}

// startRequest moves to Thinking for a new request and returns its id.
func (p *Presenter) startRequest(now time.Time) {
	p.req++
	p.since, p.failMsg = now, ""
	p.cur, p.hideAt = bubble{}, time.Time{}
	p.enterWait(Thinking)
}

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

// OnReply delivers a chat reply: a small hop, then the answer is typed out
// while Tunas talks (bicara).
func (p *Presenter) OnReply(text string, now time.Time) {
	p.deliver(bubble{text: text, kind: kindReply, d: replyFor(text)}, now)
}

// OnSearchReply delivers a web-search answer: the same, with the magnifier
// (bicara_cari), and the sources (site names) after the text is typed out.
func (p *Presenter) OnSearchReply(text string, sources []string, now time.Time) {
	p.deliver(bubble{text: text, kind: kindReply, d: replyFor(text), search: true, sources: sources}, now)
}

func (p *Presenter) deliver(b bubble, now time.Time) {
	if p.Waiting() {
		p.showReply(b, now)
		return
	}
	p.offer(b, now) // a late reply: treat like any other important bubble
}

func replyFor(text string) time.Duration {
	d := max(minReplyFor, time.Duration(utf8.RuneCountInString(text))*60*time.Millisecond)
	return min(d, maxReplyFor)
}

func (p *Presenter) showReply(b bubble, now time.Time) {
	p.State, p.cur, p.shownAt, p.typed, p.revealAll = Reply, b, now, true, p.NoAnimations
	p.Draft, p.scrollUp, p.stopped = "", 0, false
	// the text box opens under the answer; idle time counts from when the
	// answer has been typed out
	p.typedAt = now.Add(p.revealDur(b.text))
	p.hideAt = time.Time{}
	p.remember(Turn{Role: "assistant", Text: b.text})
}

// SpeechFor is how long an answer of n characters takes to type out, and
// how long Tunas talks: n × ms per character, between the minimum and the
// maximum (a long answer is typed faster, never slower than the maximum).
func (p *Presenter) SpeechFor(n int) time.Duration {
	ms, lo, hi := p.SpeechMsPerChar, p.SpeechMin, p.SpeechMax
	if ms <= 0 {
		ms = DefaultMsPerChar
	}
	if lo <= 0 {
		lo = DefaultSpeechMin
	}
	if hi < lo {
		hi = max(DefaultSpeechMax, lo)
	}
	return min(max(time.Duration(n)*ms, lo), hi)
}

// revealDur is how long the hop and the typing effect take.
func (p *Presenter) revealDur(text string) time.Duration {
	if p.revealAll {
		return 0
	}
	return HopFor + p.SpeechFor(utf8.RuneCountInString(text))
}

// Revealed is how many runes of the bubble text are visible: -1 for all,
// 0 during the hop (no bubble yet), then growing evenly over SpeechFor.
func (p *Presenter) Revealed(now time.Time) int {
	if p.State != Reply || !p.typed || p.revealAll {
		return -1
	}
	t := now.Sub(p.shownAt) - HopFor
	if t < 0 {
		return 0
	}
	total := utf8.RuneCountInString(p.cur.text)
	n := int(float64(total) * float64(t) / float64(p.SpeechFor(total)))
	if n >= total {
		return -1
	}
	return n
}

// Speaking reports whether Tunas is talking: after the hop, while the answer
// is typed out (plus a short closing nod). It ends at once when the bubble
// closes, the owner presses Enter or types, or a reminder takes over.
func (p *Presenter) Speaking(now time.Time) bool {
	if p.State != Reply || !p.typed || p.stopped || p.NoAnimations {
		return false
	}
	start := p.shownAt.Add(HopFor)
	total := utf8.RuneCountInString(p.cur.text)
	return !now.Before(start) && now.Before(start.Add(p.SpeechFor(total)+NodFor))
}

// stopSpeaking ends the gesture for good (the text is shown in full).
func (p *Presenter) stopSpeaking() {
	if p.State == Reply && p.typed {
		p.stopped = true
	}
}

// sentenceEnd reports whether rune i of rs ends a sentence.
func sentenceEnd(rs []rune, i int) bool {
	switch rs[i] {
	case '.', '!', '?':
		return i+1 >= len(rs) || rs[i+1] == ' ' || rs[i+1] == '\n'
	}
	return false
}

// nodAt is the body's nod in [0,1] at progress r (runes typed so far) of a
// text typed over d: a short dip after each sentence end, and a last one
// when the text is complete.
func nodAt(text []rune, r int, d time.Duration, sinceEnd time.Duration) float64 {
	if len(text) == 0 {
		return 0
	}
	per := d / time.Duration(len(text))
	last := -1
	for i := min(r, len(text)) - 1; i >= 0; i-- {
		if sentenceEnd(text, i) {
			last = i
			break
		}
	}
	var since time.Duration
	switch {
	case r >= len(text) && sentenceEnd(text, len(text)-1):
		since = sinceEnd
	case last >= 0:
		since = time.Duration(r-1-last) * per
	default:
		return 0
	}
	if since < 0 || since >= NodFor {
		return 0
	}
	return math.Sin(math.Pi * float64(since) / float64(NodFor))
}

// SkipReveal shows the whole answer at once (Enter while it is typed).
func (p *Presenter) SkipReveal(now time.Time) {
	if p.State != Reply || p.revealAll {
		return
	}
	p.stopSpeaking()
	p.revealAll = true
	if p.typed {
		p.typedAt = now
	} else {
		p.hideAt = now.Add(p.cur.d)
	}
}

// OnProposal shows the server's interpretation of a reminder/task request
// and waits for Enter (save) or Esc (cancel). Nothing is saved before that.
func (p *Presenter) OnProposal(question, id string, now time.Time) {
	if !p.Waiting() {
		return // the owner moved on; the server drops the proposal after 10 min
	}
	p.State, p.confirmID, p.confirmQ, p.typedAt = Confirm, id, question, now
}

// Accept confirms the proposal: it returns the id to send and waits for the
// server's real result ("" if there is no proposal).
func (p *Presenter) Accept(now time.Time) string {
	if p.State != Confirm {
		return ""
	}
	id := p.confirmID
	p.confirmID, p.sent = "", ""
	p.startRequest(now)
	return id
}

// Decline cancels the proposal locally and returns the id to report.
func (p *Presenter) Decline(now time.Time) string {
	if p.State != Confirm {
		return ""
	}
	id := p.confirmID
	p.confirmID, p.sent = "", ""
	p.show(bubble{text: TextCancelled, kind: kindReply, d: DefaultShow}, now)
	return id
}

// OnChatFailed reports a failed chat with a user-facing reason: a short sad
// pose, the reason, and the text box again with the message that was sent
// (Enter retries).
func (p *Presenter) OnChatFailed(reason string, now time.Time) {
	if !p.Waiting() {
		return // nobody is waiting for it any more
	}
	p.fail(reason, now)
}

func (p *Presenter) fail(reason string, now time.Time) {
	p.State, p.failMsg, p.Draft, p.failedAt = Failed, reason, p.sent, now
	p.cur, p.hideAt, p.typedAt = bubble{}, time.Time{}, now
}

// CancelWait abandons the pending request (Esc while loading). It returns
// false when nothing was pending. Results of the old request are ignored.
func (p *Presenter) CancelWait(now time.Time) bool {
	if !p.Waiting() {
		return false
	}
	p.req++ // the old result is stale now
	p.next(now)
	return true
}

// offer routes a new notification or late reply.
func (p *Presenter) offer(b bubble, now time.Time) {
	if b.kind == kindReminder && p.State == Reply && p.typed && !p.revealAll {
		// ingat beats bicara: Tunas stops talking and the answer is shown in
		// full; the reminder waits in the queue and comes next
		p.stopSpeaking()
		p.revealAll, p.typedAt = true, now
	}
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
	default: // Input, waiting, Failed, Reply, Confirm
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

// OnHotkey opens the text box. When the bubble already has one (input,
// retry, conversation) it only brings it back to focus; Esc closes it.
// Ignored while a request is pending (Esc cancels it instead).
func (p *Presenter) OnHotkey(now time.Time) {
	switch {
	case p.State == WorklogAsk:
		p.FocusWorklog(now)
		return
	case p.State == TegurAsk:
		p.FocusTegur(now)
		return
	case p.State == WorklogSaving:
		return
	case p.Typing():
		if p.State == Reply {
			p.stopSpeaking()
			p.revealAll = true // the reply is being typed out: show it, then type
		}
		p.typedAt = now
		return
	case p.Waiting(), p.State == Confirm:
		return
	case p.State == Showing, p.State == Reply:
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
	p.State, p.Draft, p.cur, p.hideAt, p.sent, p.scrollUp = Input, "", bubble{}, time.Time{}, "", 0
	p.typedAt = now
}

// Type appends runes to the draft.
func (p *Presenter) Type(rs []rune, now time.Time) {
	if !p.Typing() {
		return
	}
	p.stopSpeaking()
	p.revealAll = true // typing under a reply shows the whole reply
	p.typedAt, p.scrollUp = now, 0
	for _, r := range rs {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if utf8.RuneCountInString(p.Draft) >= p.draftCap() {
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
	p.stopSpeaking()
	p.revealAll = true
	p.typedAt, p.scrollUp = now, 0
	if p.Draft != "" {
		rs := []rune(p.Draft)
		p.Draft = string(rs[:len(rs)-1])
	}
}

// Submit sends the draft: it returns the text to send ("" if there is
// nothing to send, including while a request is pending).
func (p *Presenter) Submit(now time.Time) string {
	if !p.Typing() || p.State == WorklogInput {
		return "" // the worklog box is SubmitWorklog's
	}
	text := strings.TrimSpace(p.Draft)
	p.Draft = ""
	if text == "" {
		if p.State == Reply {
			p.revealAll, p.typedAt = true, now // Enter on an empty line: show it all, stay
			return ""
		}
		p.next(now)
		return ""
	}
	if !p.closedAt.IsZero() && now.Sub(p.closedAt) > ContextReset {
		p.history = nil // a new conversation
	}
	p.closedAt = time.Time{}
	p.asked = append([]Turn(nil), p.history...)
	p.remember(Turn{Role: "user", Text: text})
	p.sent = text
	p.startRequest(now)
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
	case WorklogAsk, WorklogInput, WorklogSaving:
		p.worklogTick(now)
	case TegurAsk:
		p.tegurTick(now)
	case Input, Failed:
		if now.Sub(p.typedAt) >= p.idleClose() {
			p.CancelInput(now)
		}
	case Thinking, Slow, VerySlow:
		w := now.Sub(p.since)
		switch {
		case w >= ThinkLimit:
			p.fail(FailText(agent.FailTimeout), now)
		case w >= p.verySlowAfter() && p.State != VerySlow:
			p.enterWait(VerySlow)
		case w >= p.slowAfter() && w < p.verySlowAfter() && p.State == Thinking:
			p.enterWait(Slow)
		}
	case Reply:
		switch {
		case p.typed && !p.revealAll && p.Revealed(now) == -1:
			p.revealAll = true // typed out: the text box appears
		case p.typed && p.revealAll && now.Sub(p.typedAt) >= p.idleClose():
			p.CancelInput(now)
		case !p.typed && !now.Before(p.hideAt):
			p.next(now)
		}
	case Showing:
		if !now.Before(p.hideAt) {
			p.next(now)
		}
	case Confirm:
		if now.Sub(p.typedAt) >= p.idleClose() {
			p.confirmID = ""
			p.next(now) // unanswered: nothing saved
		}
	}
}

// BubbleText is the text the one bubble lays out ("" = no bubble). While
// waiting it ends in "..." and the renderer animates the dots (WithDots);
// a reply is laid out in full and typed out by the renderer (RevealLines),
// so the bubble never changes size.
func (p *Presenter) BubbleText() string {
	switch p.State {
	case Input:
		return "› " + p.Draft + Cursor
	case Failed:
		return p.failMsg + "\n› " + p.Draft + Cursor
	case Thinking, Slow:
		return p.phaseLine
	case VerySlow:
		return p.phaseLine + "\n" + CancelHint
	case Reply:
		if p.typed && p.revealAll {
			// the sources appear once the text is complete
			return p.cur.text + p.sourcesLine() + "\n› " + p.Draft + Cursor
		}
		return p.cur.text
	case Showing:
		return p.cur.text
	case WorklogAsk, WorklogInput, WorklogSaving:
		return p.worklogText()
	case TegurAsk:
		return p.tegurBubble()
	case Confirm:
		return p.confirmQ + "\n" + ConfirmHint
	}
	return ""
}

// sourcesLine is "\n\nSumber: [1] kompas.com [2] detik.com" ("" without sources).
func (p *Presenter) sourcesLine() string {
	if len(p.cur.sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nSumber:")
	for i, s := range p.cur.sources {
		fmt.Fprintf(&b, " [%d] %s", i+1, s)
	}
	return b.String()
}

// Look is what the renderer shows for the character right now.
type Look struct {
	Form  forms.Form // a display form (Mikir, Sedih, Senang for the hop) or the agent's form
	Phase int        // Mikir: 0 mikir, 1 mikir lama, 2 sangat lama
	T     float64    // seconds in this look (drives its animation)
	Hop   float64    // success hop progress in (0,1), 0 = none
	Talk  float64    // Bicara: characters typed so far (fractional): the mouth follows it
	Nod   float64    // Bicara: the body's nod at the end of a sentence, 0..1
	Env   float64    // Bicara: 1 while talking, fading to 0 as the mouth closes
}

// Look returns the character's pose for now.
func (p *Presenter) Look(now time.Time) Look {
	if d := now.Sub(p.hopAt); !p.hopAt.IsZero() && !p.NoAnimations && d >= 0 && d < HopFor {
		return Look{Form: forms.Senang, T: 10, Hop: float64(d) / float64(HopFor)}
	}
	switch p.State {
	case WorklogAsk, WorklogInput:
		return Look{Form: forms.Ingat, T: 10}
	case TegurAsk:
		return Look{Form: forms.Tegur, T: now.Sub(p.typedAt).Seconds()}
	case Thinking, Slow, VerySlow:
		return Look{Form: forms.Mikir, Phase: int(p.State - Thinking), T: now.Sub(p.since).Seconds()}
	case Failed:
		if d := now.Sub(p.failedAt); d < SadFor {
			return Look{Form: forms.Sedih, T: d.Seconds()}
		}
	case Reply:
		if d := now.Sub(p.shownAt); p.typed && !p.NoAnimations && d < HopFor {
			return Look{Form: forms.Senang, T: 10, Hop: float64(d) / float64(HopFor)}
		}
		if p.Speaking(now) {
			return p.speakLook(now)
		}
	}
	return Look{Form: p.Form}
}

// speakLook is the bicara pose: the mouth opens and closes with the typed
// characters, the body nods at sentence ends, and the mouth closes smoothly
// at the end (the "halus" return to the previous form).
func (p *Presenter) speakLook(now time.Time) Look {
	text := []rune(p.cur.text)
	total := len(text)
	d := p.SpeechFor(total)
	start := p.shownAt.Add(HopFor)
	t := now.Sub(start)
	talk := min(float64(total)*float64(t)/float64(d), float64(total))
	end := start.Add(d)
	env := 1.0
	if left := end.Add(NodFor).Sub(now); left < speechClose {
		env = max(float64(left)/float64(speechClose), 0)
	}
	form := forms.Bicara
	if p.cur.search {
		form = forms.BicaraCari
	}
	return Look{Form: form, T: t.Seconds(), Talk: talk, Env: env,
		Nod: nodAt(text, int(talk), d, now.Sub(end))}
}

// WithDots returns lines with the trailing dots of the last line set to n
// (1..3). n == 0 returns lines unchanged.
func WithDots(lines []string, n int) []string {
	if n <= 0 || len(lines) == 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	// the dots belong to the phase text: with a hint line under it, that is
	// the line before the hint
	i := len(out) - 1
	if i > 0 && out[i] == CancelHint {
		i--
	}
	out[i] = strings.TrimRight(out[i], ".") + strings.Repeat(".", n)
	return out
}

// RevealLines keeps the first n runes of the wrapped lines (n < 0 = all).
func RevealLines(lines []string, n int) []string {
	if n < 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		rs := []rune(l)
		if n <= len(rs) {
			out = append(out, string(rs[:n]))
			break
		}
		out = append(out, l)
		n -= len(rs)
	}
	return out
}

func (p *Presenter) show(b bubble, now time.Time) {
	p.State = Showing
	if b.kind == kindReply {
		p.State = Reply
	}
	p.typed, p.revealAll, p.shownAt = false, true, now // only chat answers are typed out
	p.cur, p.hideAt = b, now.Add(b.d)
}

// next shows the oldest queued bubble, or goes idle.
func (p *Presenter) next(now time.Time) {
	p.sent, p.scrollUp, p.closedAt = "", 0, now
	if len(p.queue) > 0 {
		b := p.queue[0]
		p.queue = p.queue[1:]
		p.show(b, now)
		return
	}
	p.State, p.cur, p.hideAt = Idle, bubble{}, time.Time{}
}
