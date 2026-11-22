// Package forms is Tunas's state machine: signals in, form (mood/shape) out.
// It is pure (no I/O, injected clock and randomness) and shared by agent and server.
package forms

import (
	"math/rand/v2"
	"time"
)

// Form identifies Tunas's current form.
type Form string

// Forms in the PRD.
const (
	Biasa  Form = "biasa"
	Fokus  Form = "fokus"
	Tidur  Form = "tidur"
	Senang Form = "senang"
	Ingat  Form = "ingat"
	Tegur  Form = "tegur"
)

// All lists every form.
var All = []Form{Biasa, Fokus, Tidur, Senang, Ingat, Tegur}

// Valid reports whether f is a known form.
func Valid(f Form) bool {
	for _, x := range All {
		if x == f {
			return true
		}
	}
	return false
}

// Config holds thresholds (from the agent YAML).
type Config struct {
	TegurAfter     time.Duration // distract streak before tegur (30m)
	TegurCooldown  time.Duration // min time between tegur (45m)
	TegurFor       time.Duration // how long tegur is shown
	TidurAfterIdle time.Duration // idle before tidur (10m)
	SenangFor      time.Duration // how long senang is shown (5s)
	ReminderFor    time.Duration // how long ingat is shown
	IdleMotionMin  time.Duration // random idle motion interval on biasa (3m..7m)
	IdleMotionMax  time.Duration
}

// Signals are the inputs at one instant.
type Signals struct {
	Now            time.Time
	Category       string        // category of the foreground app
	Idle           time.Duration // time since last input
	ReminderText   string        // latest due reminder text
	ReminderAt     time.Time     // when the reminder was received (zero = none)
	TaskDoneAt     time.Time     // when a task was last completed (zero = none)
	DistractStreak time.Duration // consecutive time in 'distract'
	Override       Form          // manual override ("" = auto)
}

// Output is what Tunas should show.
type Output struct {
	Form     Form
	Speech   string        // bubble text ("" = none)
	Duration time.Duration // how long to show the bubble/form (0 = while condition holds)
	Motion   bool          // trigger a random idle motion now (biasa only)
}

// Machine holds the little state the rules need (tegur cooldown, idle motion timer).
type Machine struct {
	cfg        Config
	rng        *rand.Rand
	lastTegur  time.Time
	tegurUntil time.Time
	nextMotion time.Time
}

// New creates a machine. rng may be nil (seeded randomly).
func New(cfg Config, rng *rand.Rand) *Machine {
	if rng == nil {
		rng = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	return &Machine{cfg: cfg, rng: rng}
}

// Step evaluates the rules in priority order.
func (m *Machine) Step(s Signals) Output {
	now := s.Now
	if s.Override != "" && Valid(s.Override) {
		return Output{Form: s.Override}
	}
	if !s.ReminderAt.IsZero() && within(now, s.ReminderAt, m.cfg.ReminderFor) {
		return Output{Form: Ingat, Speech: s.ReminderText, Duration: remaining(now, s.ReminderAt, m.cfg.ReminderFor)}
	}
	if !s.TaskDoneAt.IsZero() && within(now, s.TaskDoneAt, m.cfg.SenangFor) {
		return Output{Form: Senang, Speech: "Yeay, satu tugas beres! 🎉", Duration: remaining(now, s.TaskDoneAt, m.cfg.SenangFor)}
	}
	if now.Before(m.tegurUntil) {
		return Output{Form: Tegur, Speech: tegurSpeech, Duration: m.tegurUntil.Sub(now)}
	}
	if s.DistractStreak >= m.cfg.TegurAfter && (m.lastTegur.IsZero() || now.Sub(m.lastTegur) >= m.cfg.TegurCooldown) {
		m.lastTegur = now
		m.tegurUntil = now.Add(m.cfg.TegurFor)
		return Output{Form: Tegur, Speech: tegurSpeech, Duration: m.cfg.TegurFor}
	}
	if s.Idle >= m.cfg.TidurAfterIdle {
		return Output{Form: Tidur, Speech: "Zzz..."}
	}
	if s.Category == "work" {
		return Output{Form: Fokus}
	}
	out := Output{Form: Biasa}
	if m.nextMotion.IsZero() {
		m.scheduleMotion(now)
	} else if !now.Before(m.nextMotion) {
		out.Motion = true
		m.scheduleMotion(now)
	}
	return out
}

const tegurSpeech = "Sudah lama di hiburan, nih. Istirahat mata dulu, lalu lanjut?"

func (m *Machine) scheduleMotion(now time.Time) {
	span := m.cfg.IdleMotionMax - m.cfg.IdleMotionMin
	d := m.cfg.IdleMotionMin
	if span > 0 {
		d += time.Duration(m.rng.Int64N(int64(span) + 1))
	}
	m.nextMotion = now.Add(d)
}

func within(now, at time.Time, d time.Duration) bool {
	return !now.Before(at) && now.Sub(at) < d
}

func remaining(now, at time.Time, d time.Duration) time.Duration { return at.Add(d).Sub(now) }

// Streak tracks consecutive time in the distract category.
type Streak struct {
	d time.Duration
}

// Add records a sample of length dt. Any active non-distract sample resets the
// streak; idle samples neither add nor reset.
func (s *Streak) Add(category string, idle bool, dt time.Duration) time.Duration {
	switch {
	case idle:
	case category == "distract":
		s.d += dt
	default:
		s.d = 0
	}
	return s.d
}
