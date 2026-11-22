// Package overlay draws Tunas on screen. This file holds the pure,
// platform-independent logic (text wrapping, bubble geometry, window
// placement, show/hide decisions) so it can be tested anywhere; the
// Ebitengine/Win32 parts are in *_windows.go.
package overlay

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/forms"
)

// Rect is an axis-aligned rectangle in window (logical) pixels.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether (px, py) lies inside r.
func (r Rect) Contains(px, py float64) bool {
	return px >= r.X && px < r.X+r.W && py >= r.Y && py < r.Y+r.H
}

// Wrap breaks text into lines no wider than maxW according to measure.
// Words longer than maxW are split by runes. Explicit newlines are kept.
func Wrap(text string, maxW float64, measure func(string) float64) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := ""
		for _, w := range words {
			for measure(w) > maxW && utf8.RuneCountInString(w) > 1 {
				// split an over-long word
				head, tail := splitToFit(w, maxW, measure)
				if cur != "" {
					lines = append(lines, cur)
					cur = ""
				}
				lines = append(lines, head)
				w = tail
			}
			cand := w
			if cur != "" {
				cand = cur + " " + w
			}
			if cur != "" && measure(cand) > maxW {
				lines = append(lines, cur)
				cur = w
			} else {
				cur = cand
			}
		}
		lines = append(lines, cur)
	}
	return lines
}

func splitToFit(w string, maxW float64, measure func(string) float64) (string, string) {
	rs := []rune(w)
	n := 1
	for n < len(rs) && measure(string(rs[:n+1])) <= maxW {
		n++
	}
	return string(rs[:n]), string(rs[n:])
}

// Layout constants (logical pixels at scale 1).
const (
	WindowW      = 340
	WindowH      = 300
	CharW        = 88 // body ellipse ≈ 88×76 plus leaf
	CharH        = 110
	BubbleMaxW   = 240
	BubblePad    = 12
	BubbleRadius = 14
	TailH        = 12
	LineHeight   = 20
	MaxLines     = 7
)

// Scene is the computed geometry for one frame.
type Scene struct {
	Char   Rect
	Bubble Rect     // zero W when no bubble
	Lines  []string // wrapped text
	TailX  float64  // x of the tail tip (points at the character)
	Right  bool     // character on the right side of the window
}

// ComputeScene places the character at the bottom of the window (right or
// left, matching the screen corner) and the bubble above it.
func ComputeScene(corner, text string, measure func(string) float64) Scene {
	right := strings.HasSuffix(corner, "right")
	s := Scene{Right: right}
	cx := float64(WindowW - CharW - 8)
	if !right {
		cx = 8
	}
	s.Char = Rect{X: cx, Y: WindowH - CharH, W: CharW, H: CharH}
	if text == "" {
		return s
	}
	lines := Wrap(text, BubbleMaxW-2*BubblePad, measure)
	if len(lines) > MaxLines {
		lines = lines[:MaxLines]
		last := []rune(lines[MaxLines-1])
		if len(last) > 1 {
			last = last[:len(last)-1]
		}
		lines[MaxLines-1] = string(last) + "…"
	}
	w := 0.0
	for _, l := range lines {
		w = max(w, measure(l))
	}
	bw := min(float64(BubbleMaxW), w+2*BubblePad)
	bw = max(bw, 48)
	bh := float64(len(lines))*LineHeight + 2*BubblePad
	bx := s.Char.X + s.Char.W - bw // right-aligned with the character
	if !right {
		bx = s.Char.X
	}
	by := s.Char.Y - TailH - bh - 4 // clear of the upright leaf
	s.Bubble = Rect{X: bx, Y: max(by, 0), W: bw, H: bh}
	s.Lines = lines
	s.TailX = s.Char.X + s.Char.W/2
	return s
}

// WindowPosition returns the window's top-left on a monitor of size (mw, mh)
// in device-independent pixels.
func WindowPosition(corner string, mw, mh, marginX, marginY int) (int, int) {
	x, y := mw-WindowW-marginX, mh-WindowH-marginY
	if strings.HasSuffix(corner, "left") {
		x = marginX
	}
	if strings.HasPrefix(corner, "top") {
		y = marginY
	}
	return max(x, 0), max(y, 0)
}

// Presenter decides what is visible. It is driven by form changes, the
// hotkey and chat replies; the clock is passed in.
type Presenter struct {
	Visible bool
	Form    forms.Form
	Speech  string
	Input   bool   // text input mode (after the hotkey)
	Draft   string // text being typed
	hideAt  time.Time
	typedAt time.Time // last input activity
}

// Default display durations.
const (
	DefaultShow  = 6 * time.Second
	minReplyFor  = 8 * time.Second
	MaxDraftLen  = 300
	InputTimeout = 60 * time.Second // input mode is cancelled after this long without typing
)

// OnForm reacts to a new state-machine output. Forms with something to say
// are shown; quiet forms only update the pose if already visible.
func (p *Presenter) OnForm(out forms.Output, now time.Time) {
	p.Form = out.Form
	if p.Input {
		return
	}
	if out.Speech == "" {
		return
	}
	if out.Form == forms.Tidur && !p.Visible {
		return // do not pop up just to say Zzz
	}
	d := out.Duration
	if d <= 0 {
		d = DefaultShow
	}
	p.show(out.Speech, now, d)
}

// OnHotkey toggles text input mode.
func (p *Presenter) OnHotkey(now time.Time) {
	if p.Input {
		p.CancelInput(now)
		return
	}
	p.Input, p.Draft, p.Visible = true, "", true
	p.Speech = ""
	p.hideAt = time.Time{}
	p.typedAt = now
}

// Type appends runes to the draft.
func (p *Presenter) Type(rs []rune, now time.Time) {
	if !p.Input {
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
	p.typedAt = now
	if p.Input && p.Draft != "" {
		rs := []rune(p.Draft)
		p.Draft = string(rs[:len(rs)-1])
	}
}

// Submit ends input mode and returns the text to send ("" if empty).
func (p *Presenter) Submit(now time.Time) string {
	if !p.Input {
		return ""
	}
	text := strings.TrimSpace(p.Draft)
	p.Input, p.Draft = false, ""
	if text == "" {
		p.Visible = false
		return ""
	}
	p.show("...", now, 60*time.Second) // thinking; replaced by the reply
	return text
}

// CancelInput leaves input mode and hides.
func (p *Presenter) CancelInput(time.Time) {
	p.Input, p.Draft, p.Visible, p.Speech = false, "", false, ""
}

// OnReply shows a chat reply for a duration based on its length.
func (p *Presenter) OnReply(text string, now time.Time) {
	d := max(minReplyFor, time.Duration(utf8.RuneCountInString(text))*60*time.Millisecond)
	p.show(text, now, min(d, 30*time.Second))
}

// Dismiss hides immediately (click on Tunas).
func (p *Presenter) Dismiss() {
	if !p.Input {
		p.Visible, p.Speech = false, ""
	}
}

// Tick hides the overlay when its time is up.
func (p *Presenter) Tick(now time.Time) {
	if p.Input && now.Sub(p.typedAt) >= InputTimeout {
		p.CancelInput(now)
		return
	}
	if p.Visible && !p.Input && !p.hideAt.IsZero() && !now.Before(p.hideAt) {
		p.Visible, p.Speech, p.hideAt = false, "", time.Time{}
	}
}

// BubbleText is what the bubble should display now.
func (p *Presenter) BubbleText() string {
	if p.Input {
		return "› " + p.Draft + "▏"
	}
	return p.Speech
}

func (p *Presenter) show(text string, now time.Time, d time.Duration) {
	p.Visible, p.Speech, p.hideAt = true, text, now.Add(d)
}
