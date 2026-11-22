package forms

import (
	"math/rand/v2"
	"testing"
	"time"
)

var cfg = Config{
	TegurAfter: 30 * time.Minute, TegurCooldown: 45 * time.Minute, TegurFor: 10 * time.Second,
	TidurAfterIdle: 10 * time.Minute, SenangFor: 5 * time.Second, ReminderFor: 15 * time.Second,
	IdleMotionMin: 3 * time.Minute, IdleMotionMax: 7 * time.Minute,
}

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

func newM() *Machine { return New(cfg, rand.New(rand.NewPCG(1, 2))) }

func TestPriorities(t *testing.T) {
	tests := []struct {
		name   string
		s      Signals
		want   Form
		speech string
	}{
		{"default", Signals{Category: "other"}, Biasa, ""},
		{"browse is biasa", Signals{Category: "browse"}, Biasa, ""},
		{"work", Signals{Category: "work"}, Fokus, ""},
		{"idle 9m still fokus", Signals{Category: "work", Idle: 9 * time.Minute}, Fokus, ""},
		{"idle 10m", Signals{Category: "work", Idle: 10 * time.Minute}, Tidur, "Zzz..."},
		{"tegur over tidur", Signals{Idle: 20 * time.Minute, DistractStreak: 31 * time.Minute}, Tegur, tegurSpeech},
		{"distract 29m", Signals{Category: "distract", DistractStreak: 29 * time.Minute}, Biasa, ""},
		{"distract 30m", Signals{Category: "distract", DistractStreak: 30 * time.Minute}, Tegur, tegurSpeech},
		{"senang over tegur", Signals{DistractStreak: time.Hour, TaskDoneAt: t0.Add(-2 * time.Second)}, Senang, "Yeay, satu tugas beres! 🎉"},
		{"senang expired", Signals{Category: "work", TaskDoneAt: t0.Add(-5 * time.Second)}, Fokus, ""},
		{"ingat over senang", Signals{TaskDoneAt: t0, ReminderAt: t0.Add(-time.Second), ReminderText: "minum"}, Ingat, "minum"},
		{"ingat expired", Signals{Category: "work", ReminderAt: t0.Add(-15 * time.Second), ReminderText: "x"}, Fokus, ""},
		{"override wins", Signals{Override: Tidur, ReminderAt: t0, ReminderText: "x"}, Tidur, ""},
		{"invalid override ignored", Signals{Override: "naga", Category: "work"}, Fokus, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.s.Now = t0
			got := newM().Step(tt.s)
			if got.Form != tt.want || got.Speech != tt.speech {
				t.Fatalf("Step = %+v, want %s %q", got, tt.want, tt.speech)
			}
		})
	}
}

func TestDurations(t *testing.T) {
	m := newM()
	out := m.Step(Signals{Now: t0, ReminderAt: t0.Add(-5 * time.Second), ReminderText: "x"})
	if out.Duration != 10*time.Second {
		t.Fatalf("ingat duration = %v", out.Duration)
	}
}

func TestTegurCooldown(t *testing.T) {
	m := newM()
	streak := 30 * time.Minute
	step := func(at time.Time) Form {
		return m.Step(Signals{Now: at, Category: "distract", DistractStreak: streak}).Form
	}
	if step(t0) != Tegur {
		t.Fatal("first tegur")
	}
	if step(t0.Add(5*time.Second)) != Tegur {
		t.Fatal("tegur should stay for TegurFor")
	}
	if step(t0.Add(10*time.Second)) != Biasa {
		t.Fatal("tegur should end after TegurFor")
	}
	if step(t0.Add(44*time.Minute)) != Biasa {
		t.Fatal("cooldown not respected")
	}
	if step(t0.Add(45*time.Minute)) != Tegur {
		t.Fatal("tegur after cooldown")
	}
}

func TestIdleMotion(t *testing.T) {
	m := newM()
	if m.Step(Signals{Now: t0}).Motion {
		t.Fatal("no motion on first step")
	}
	var fired []time.Duration
	for s := 10 * time.Second; s <= 30*time.Minute; s += 10 * time.Second {
		if m.Step(Signals{Now: t0.Add(s)}).Motion {
			fired = append(fired, s)
		}
	}
	if len(fired) < 4 || len(fired) > 10 {
		t.Fatalf("motions in 30m = %v", fired)
	}
	prev := time.Duration(0)
	for _, f := range fired {
		if gap := f - prev; gap < 3*time.Minute || gap > 7*time.Minute+10*time.Second {
			t.Fatalf("gap %v out of 3..7m (%v)", gap, fired)
		}
		prev = f
	}
	// fokus never moves
	if m.Step(Signals{Now: t0.Add(time.Hour), Category: "work"}).Motion {
		t.Fatal("motion while fokus")
	}
}

func TestStreak(t *testing.T) {
	var s Streak
	dt := 10 * time.Second
	for i := 0; i < 6; i++ {
		s.Add("distract", false, dt)
	}
	if got := s.Add("distract", true, dt); got != time.Minute {
		t.Fatalf("idle sample changed streak: %v", got)
	}
	if got := s.Add("work", false, dt); got != 0 {
		t.Fatalf("work should reset: %v", got)
	}
}

func TestValid(t *testing.T) {
	if !Valid(Fokus) || Valid("x") {
		t.Fatal("Valid")
	}
}
