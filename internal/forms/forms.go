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

// Display-only forms: the overlay shows them around a chat (waiting for an
// answer, a failed request). The state machine never outputs them and they
// cannot be set with /form, so they are not in All and not Valid.
const (
	Mikir      Form = "mikir"
	Sedih      Form = "sedih"
	Bicara     Form = "bicara"      // talking: shown while an answer is typed out
	BicaraCari Form = "bicara_cari" // talking with a magnifier: the answer came from a web search
)

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
	TidurAfterIdle time.Duration // idle before tidur (10m)
	SenangFor      time.Duration // how long senang is shown (5s)
	ReminderFor    time.Duration // how long ingat is shown
	IdleMotionMin  time.Duration // random idle motion interval on biasa (3m..7m)
	IdleMotionMax  time.Duration
}

// Signals are the inputs at one instant.
type Signals struct {
	Now          time.Time
	Category     string        // category of the foreground app
	Idle         time.Duration // time since last input
	ReminderText string        // latest due reminder text
	ReminderAt   time.Time     // when the reminder was received (zero = none)
	TaskDoneAt   time.Time     // when a task was last completed (zero = none)
	Override     Form          // manual override ("" = auto)
}

// Output is what Tunas should show.
type Output struct {
	Form     Form
	Speech   string        // bubble text ("" = none)
	Duration time.Duration // how long to show the bubble/form (0 = while condition holds)
	Motion   bool          // trigger a random idle motion now (biasa only)
}

// Machine holds the little state the rules need (the idle motion timer). The
// distraction nudge (form tegur) is not a rule here: see Nudge.
type Machine struct {
	cfg        Config
	rng        *rand.Rand
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
