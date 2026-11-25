package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/domains"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// The distraction nudge (PRD Fase 8c). The agent counts how long the owner has
// been on apps or sites of the distract category; the pure rules are in
// forms.Nudge. When it fires the overlay asks a question with two answers.

type nudgeState struct {
	mu      sync.Mutex
	n       *forms.Nudge
	off     bool // /tegur off (the server's switch)
	lastKey string
}

// setTegurEnabled applies the server's /tegur on|off.
func (a *Agent) setTegurEnabled(on bool) {
	a.nudge.mu.Lock()
	a.nudge.off = !on
	a.nudge.mu.Unlock()
}

// domainCategory is the category of a website ("" if none), matched by suffix.
func (a *Agent) domainCategory(host string) string {
	a.dom.mu.Lock()
	defer a.dom.mu.Unlock()
	_, c, ok := domains.Match(host, a.dom.cats)
	if !ok {
		return ""
	}
	return c
}

// NudgeText is what the nudge says.
func NudgeText(key string, streak time.Duration) string {
	name := strings.TrimSuffix(key, ".exe")
	return fmt.Sprintf("Udah %d menit di %s nih. Lagi belajar atau nonton?", int(streak.Minutes()), name)
}

// stepNudge feeds one sample to the nudge and asks the overlay when it fires.
func (a *Agent) stepNudge(now time.Time, dt time.Duration, cat, key string, idle bool) {
	a.nudge.mu.Lock()
	if a.nudge.n == nil {
		a.nudge.n = forms.NewNudge(NudgeConfig(a.Cfg.Forms))
	}
	asleep := a.last.Form == forms.Tidur
	fire := a.nudge.n.Step(forms.NudgeSample{Now: now, Dt: dt, Distract: cat == "distract", Key: key, Idle: idle, Asleep: asleep, Enabled: !a.nudge.off})
	var text string
	if fire {
		a.nudge.lastKey = key
		text = NudgeText(key, a.nudge.n.Streak())
	}
	a.nudge.mu.Unlock()
	if fire && a.OnNudge != nil {
		a.Log.Info("distraction nudge", "key", key)
		a.OnNudge(text)
	}
}

// TegurAnswer is the owner's answer: working=true is "Lagi belajar/kerja"
// (quiet about that app or site for a while), false is "Oke, aku stop".
func (a *Agent) TegurAnswer(working bool) {
	a.nudge.mu.Lock()
	defer a.nudge.mu.Unlock()
	if working && a.nudge.n != nil {
		a.nudge.n.SnoozeKey(a.nudge.lastKey, a.Now())
	}
}
