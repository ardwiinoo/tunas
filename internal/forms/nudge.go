package forms

import "time"

// NudgeConfig sets the distraction nudge (PRD Fase 8c): after After in a row
// on apps or sites of the distract category, Tunas asks once, then keeps quiet
// for a while. It replaces the old forms.tegur_after_minutes rule.
type NudgeConfig struct {
	After     time.Duration // distract time in a row before a nudge (30m)
	Gap       time.Duration // a break shorter than this does not end the streak (2m)
	Snooze    time.Duration // quiet for one app/site after "lagi belajar/kerja" (90m, that day only)
	Cooldown  time.Duration // minimum time between two nudges (45m)
	MaxPerDay int           // at most this many nudges a day (4)
}

// DefaultNudge is the PRD default.
func DefaultNudge() NudgeConfig {
	return NudgeConfig{After: 30 * time.Minute, Gap: 2 * time.Minute, Snooze: 90 * time.Minute, Cooldown: 45 * time.Minute, MaxPerDay: 4}
}

// NudgeSample is one sample (pure input; no clock inside).
type NudgeSample struct {
	Now      time.Time
	Dt       time.Duration // length of the sample
	Distract bool          // the foreground app or site is in the distract category
	Key      string        // that app or site ("youtube.com", "Discord.exe")
	Idle     bool          // the owner is away: neither counts nor breaks the streak
	Asleep   bool          // form tidur: never nudge
	Enabled  bool          // /tegur on
}

// Nudge is the little state machine behind the nudge.
type Nudge struct {
	cfg      NudgeConfig
	streak   time.Duration
	away     time.Duration // active non-distract time since the last distract sample
	lastFire time.Time
	fired    int
	day      string
	snoozed  map[string]time.Time
}

// NewNudge creates the machine.
func NewNudge(cfg NudgeConfig) *Nudge { return &Nudge{cfg: cfg, snoozed: map[string]time.Time{}} }

// Streak is the distract time in a row so far.
func (n *Nudge) Streak() time.Duration { return n.streak }

// Step takes one sample and reports whether to nudge now (about s.Key).
func (n *Nudge) Step(s NudgeSample) bool {
	if day := s.Now.Format("2006-01-02"); day != n.day {
		n.day, n.fired = day, 0 // a new day: the count and the snoozes start over
		clear(n.snoozed)
	}
	switch {
	case s.Idle:
		return false
	case s.Distract:
		n.streak += s.Dt
		n.away = 0
	default:
		n.away += s.Dt
		if n.away >= n.cfg.Gap {
			n.streak, n.away = 0, 0
		}
		return false
	}
	switch {
	case !s.Enabled, s.Asleep, n.streak < n.cfg.After, n.fired >= n.cfg.MaxPerDay:
		return false
	case !n.lastFire.IsZero() && s.Now.Sub(n.lastFire) < n.cfg.Cooldown:
		return false
	case s.Now.Before(n.snoozed[s.Key]):
		return false
	}
	n.lastFire = s.Now
	n.fired++
	return true
}

// SnoozeKey is the answer "lagi belajar/kerja": no nudge about key for the
// snooze time (until the end of the day at the latest).
func (n *Nudge) SnoozeKey(key string, now time.Time) {
	until := now.Add(n.cfg.Snooze)
	if eod := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location()); until.After(eod) {
		until = eod
	}
	n.snoozed[key] = until
}
