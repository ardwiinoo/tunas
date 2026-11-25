package overlay

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Worklog bubbles (PRD 8.4).
//
//	worklog.due   → WorklogAsk (no focus taken; the form is "ingat")
//	click / Alt+Space on it → keys: Enter = write, N = later, Esc = skip today
//	Enter         → WorklogInput (text box) → Enter → saved ✓ (or queued)
//	worklog.closed (answered in Telegram) → the ask bubble closes by itself
//
// Keys cannot reach a window without focus unless a global keyboard hook
// watches every key the owner types elsewhere, so the shortcuts work once
// the bubble is clicked or Alt+Space is pressed (see DECISIONS).

// Worklog texts.
const (
	TextWorklogAsk      = "Update worklog yuk, lagi ngerjain apa?"
	HintWorklogIdle     = "klik atau Alt+Space untuk menjawab"
	HintWorklogKeys     = "Enter = tulis · N = nanti · S = senyapkan hari ini"
	TextWorklogPrompt   = "Worklog hari ini:"
	TextWorklogMuted    = "Pengingat worklog disenyapkan untuk hari ini."
	TextWorklogTick     = "√" // Go Regular has no ✓ (U+2713); √ is in WGL4
	TextWorklogSaved    = "Tersimpan √"
	TextWorklogQueued   = "Tersimpan √ (menunggu sinkron)"
	WorklogAskFor       = 5 * time.Minute // unanswered: hide (Telegram still has it)
	logPrefix           = "/log "
	worklogSavedShowFor = 4 * time.Second
)

// WorklogAction is what a key on the ask bubble means.
type WorklogAction int

const (
	WorklogNone WorklogAction = iota
	WorklogSkip               // N or Esc: skip this slot only; the next slot still reminds
	WorklogMute               // S: no more reminders today
)

// WorklogDue shows the reminder bubble without taking focus. It only
// replaces chatter or nothing; anything else in progress wins (the
// reminder is also in Telegram).
func (p *Presenter) WorklogDue(promptID int64, now time.Time) bool {
	if p.State != Idle && !(p.State == Showing && p.cur.kind == kindChatter) {
		return false
	}
	p.State, p.wlPrompt, p.wlFocused, p.typedAt = WorklogAsk, promptID, false, now
	p.cur, p.hideAt = bubble{}, time.Time{}
	return true
}

// WorklogClosed closes the ask bubble when the prompt was answered
// elsewhere. A text box the owner is typing in stays.
func (p *Presenter) WorklogClosed(promptID int64, now time.Time) {
	if p.State == WorklogAsk && p.wlPrompt == promptID {
		p.next(now)
	}
}

// FocusWorklog is a click or Alt+Space on the ask bubble: the keys work now.
func (p *Presenter) FocusWorklog(now time.Time) bool {
	if p.State != WorklogAsk {
		return false
	}
	p.wlFocused, p.typedAt = true, now
	return true
}

// WorklogKey handles a key on the focused ask bubble. It returns the
// action to send to the server (later/skip) and the prompt id.
func (p *Presenter) WorklogKey(r rune, now time.Time) (WorklogAction, int64) {
	if p.State != WorklogAsk || !p.wlFocused {
		return WorklogNone, 0
	}
	id := p.wlPrompt
	switch r {
	case '\r':
		p.OpenWorklog(id, now)
	case 'n', 'N', 0x1B:
		p.next(now)
		return WorklogSkip, id
	case 's', 'S':
		p.next(now)
		return WorklogMute, id
	}
	return WorklogNone, 0
}

// OpenWorklog opens the worklog text box (Enter on the ask bubble, the
// tray's "Tulis worklog…"). promptID 0 = not answering a reminder.
func (p *Presenter) OpenWorklog(promptID int64, now time.Time) {
	p.State, p.wlPrompt, p.Draft, p.typedAt = WorklogInput, promptID, "", now
	p.cur, p.hideAt, p.scrollUp = bubble{}, time.Time{}, 0
}

// SubmitWorklog takes the worklog text box (or "/log text" in the chat
// box): it returns the text and the prompt it answers ("" = nothing).
func (p *Presenter) SubmitWorklog(now time.Time) (string, int64) {
	var text string
	switch p.State {
	case WorklogInput:
		text = strings.TrimSpace(p.Draft)
	case Input:
		d := strings.TrimSpace(p.Draft)
		if !IsLogCommand(d) {
			return "", 0
		}
		text = strings.TrimSpace(d[len(logPrefix)-1:])
	default:
		return "", 0
	}
	id := p.wlPrompt
	p.Draft = ""
	if text == "" {
		p.next(now)
		return "", 0
	}
	p.State, p.wlPrompt = WorklogSaving, 0
	p.since = now
	return text, id
}

// IsLogCommand reports whether chat input is "/log text" (saved straight
// to the worklog, no LLM).
func IsLogCommand(s string) bool {
	s = strings.TrimSpace(s)
	n := len(logPrefix) - 1 // "/log"
	return len(s) > n && strings.EqualFold(s[:n], "/log") && (s[n] == ' ' || s[n] == '\n')
}

// MaxPasteLen bounds a pasted worklog (runes); a paste holds up to 50 entries.
const MaxPasteLen = 8000

// multiLine reports whether the open text box takes a multi-line paste: the
// worklog box and a chat draft that is a "/log" command.
func (p *Presenter) multiLine() bool {
	return p.State == WorklogInput || (p.State == Input && IsLogCommand(p.Draft+"x"))
}

// draftCap is the draft limit in runes for the open text box.
func (p *Presenter) draftCap() int {
	if p.multiLine() {
		return MaxPasteLen
	}
	return MaxDraftLen
}

// Paste inserts clipboard text (Ctrl+V). The worklog box and a "/log" draft
// keep the line breaks (one line = one entry); anything else becomes one
// line.
func (p *Presenter) Paste(text string, now time.Time) {
	if !p.Typing() {
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !p.multiLine() {
		text = strings.Join(strings.Fields(text), " ")
	}
	var b strings.Builder
	for _, r := range text {
		if r == '\n' || r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	p.stopSpeaking()
	p.revealAll = true
	p.typedAt, p.scrollUp = now, 0
	room := p.draftCap() - utf8.RuneCountInString(p.Draft)
	if room <= 0 {
		return
	}
	add := []rune(b.String())
	if len(add) > room {
		add = add[:room]
	}
	p.Draft += string(add)
}

// WorklogCount is how many entries the last submitted worklog holds.
func (p *Presenter) WorklogCount() int { return p.wlCount }

// BeginWorklogSave moves to the saving state for a paste of n entries (the
// clipboard path, which does not go through SubmitWorklog).
func (p *Presenter) BeginWorklogSave(n int, now time.Time) {
	p.State, p.wlCount, p.confirmID, p.since = WorklogSaving, n, "", now
}

// WorklogSaved shows the outcome with a small hop.
func (p *Presenter) WorklogSaved(synced bool, reject string, now time.Time) {
	if p.State != WorklogSaving {
		return
	}
	text := TextWorklogSaved
	if p.wlCount > 1 {
		text = fmt.Sprintf("%d catatan tersimpan %s", p.wlCount, TextWorklogTick)
	}
	switch {
	case reject != "":
		p.fail("worklog ditolak: "+reject, now)
		return
	case !synced:
		text += " (menunggu sinkron)"
	}
	p.show(bubble{text: text, kind: kindReply, d: worklogSavedShowFor}, now)
	p.hopAt = now
}

func (p *Presenter) worklogText() string {
	switch p.State {
	case WorklogAsk:
		hint := HintWorklogIdle
		if p.wlFocused {
			hint = HintWorklogKeys
		}
		return TextWorklogAsk + "\n" + hint
	case WorklogInput:
		return TextWorklogPrompt + "\n› " + p.Draft + Cursor
	case WorklogSaving:
		return "menyimpan..."
	}
	return ""
}

func (p *Presenter) worklogTick(now time.Time) {
	switch p.State {
	case WorklogAsk:
		limit := WorklogAskFor
		if p.wlFocused {
			limit = p.idleClose()
		}
		if now.Sub(p.typedAt) >= limit {
			p.next(now)
		}
	case WorklogInput:
		if now.Sub(p.typedAt) >= p.idleClose() {
			p.next(now)
		}
	case WorklogSaving:
		if now.Sub(p.since) >= ThinkLimit {
			p.fail(FailText("timeout"), now)
		}
	}
}

// AskLocal asks a yes/no question of the agent's own (e.g. autostart);
// it only appears when nothing else is going on.
func (p *Presenter) AskLocal(question, id string, now time.Time) bool {
	if p.State != Idle {
		return false
	}
	p.State, p.confirmID, p.confirmQ, p.typedAt = Confirm, id, question, now
	return true
}

// Note shows a short message (the answer to a local question).
func (p *Presenter) Note(text string, now time.Time) {
	p.confirmID = ""
	p.show(bubble{text: text, kind: kindReply, d: DefaultShow}, now)
}
