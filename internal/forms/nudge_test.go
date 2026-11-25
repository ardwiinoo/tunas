package forms

import (
	"testing"
	"time"
)

// nudgeRun feeds a Nudge with 10 s samples from a fake clock.
type nudgeRun struct {
	t       *testing.T
	n       *Nudge
	now     time.Time
	enabled bool
	fires   []time.Time
}

func newRun(t *testing.T) *nudgeRun {
	return &nudgeRun{t: t, n: NewNudge(DefaultNudge()), now: time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC), enabled: true}
}

// feed runs d of samples; distract says what the owner is doing. It returns
// whether a nudge fired during it.
func (r *nudgeRun) feed(d time.Duration, distract bool, key string, idle bool) bool {
	fired := false
	for end := r.now.Add(d); r.now.Before(end); {
		r.now = r.now.Add(10 * time.Second)
		if r.n.Step(NudgeSample{Now: r.now, Dt: 10 * time.Second, Distract: distract, Key: key, Idle: idle, Enabled: r.enabled}) {
			r.fires = append(r.fires, r.now)
			fired = true
		}
	}
	return fired
}

func TestNudgeAfterThirtyMinutesInARow(t *testing.T) {
	r := newRun(t)
	if r.feed(29*time.Minute+50*time.Second, true, "youtube.com", false) {
		t.Fatal("nudged before 30 minutes")
	}
	if !r.feed(10*time.Second, true, "youtube.com", false) {
		t.Fatal("no nudge at 30 minutes")
	}
	if len(r.fires) != 1 {
		t.Fatalf("%d nudges", len(r.fires))
	}
}

func TestShortBreaksDoNotEndTheStreak(t *testing.T) {
	r := newRun(t)
	r.feed(20*time.Minute, true, "youtube.com", false)
	r.feed(110*time.Second, false, "", false) // under 2 minutes elsewhere
	r.feed(20*time.Second, true, "youtube.com", false)
	if r.n.Streak() < 20*time.Minute {
		t.Fatalf("a break under 2 minutes reset the streak: %v", r.n.Streak())
	}
	if !r.feed(10*time.Minute, true, "youtube.com", false) {
		t.Fatal("the streak should have reached 30 minutes")
	}
	// a break of 2 minutes or more starts over
	r2 := newRun(t)
	r2.feed(25*time.Minute, true, "youtube.com", false)
	r2.feed(2*time.Minute, false, "", false)
	if r2.n.Streak() != 0 {
		t.Fatalf("a 2 minute break must end the streak, got %v", r2.n.Streak())
	}
	if r2.feed(29*time.Minute, true, "youtube.com", false) {
		t.Fatal("nudged without 30 fresh minutes")
	}
}

func TestIdleNeitherCountsNorBreaks(t *testing.T) {
	r := newRun(t)
	r.feed(20*time.Minute, true, "youtube.com", false)
	r.feed(40*time.Minute, false, "", true) // away from the keyboard
	if got := r.n.Streak(); got != 20*time.Minute {
		t.Fatalf("idle changed the streak: %v", got)
	}
	// and a nudge never fires while idle, even with a long streak
	r.n.streak = time.Hour
	if r.feed(time.Minute, true, "youtube.com", true) {
		t.Fatal("nudged while idle")
	}
}

func TestNudgeCooldownAndDailyLimit(t *testing.T) {
	r := newRun(t)
	r.feed(30*time.Minute, true, "youtube.com", false)
	if len(r.fires) != 1 {
		t.Fatalf("first nudge: %d", len(r.fires))
	}
	first := r.fires[0]
	r.feed(44*time.Minute, true, "youtube.com", false) // still going: under the 45 minute cooldown
	if len(r.fires) != 1 {
		t.Fatalf("nudged again within the cooldown: %v", r.fires)
	}
	r.feed(2*time.Minute, true, "youtube.com", false)
	if len(r.fires) != 2 || r.fires[1].Sub(first) < 45*time.Minute {
		t.Fatalf("second nudge: %v", r.fires)
	}
	// at most four a day
	r.feed(12*time.Hour, true, "youtube.com", false)
	day := map[string]int{}
	for _, f := range r.fires {
		day[f.Format("2006-01-02")]++
	}
	for d, n := range day {
		if n > 4 {
			t.Fatalf("%d nudges on %s", n, d)
		}
	}
	if day["2026-11-25"] != 4 {
		t.Fatalf("expected the daily limit to be reached: %v", day)
	}
	// the next day starts over
	r.now = time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	before := len(r.fires)
	r.feed(40*time.Minute, true, "youtube.com", false)
	if len(r.fires) != before+1 {
		t.Fatalf("a new day should allow a nudge again: %d → %d", before, len(r.fires))
	}
}

func TestSnoozeIsPerSiteAndLastsTheSnoozeTime(t *testing.T) {
	r := newRun(t)
	r.feed(30*time.Minute, true, "youtube.com", false)
	r.n.SnoozeKey("youtube.com", r.now) // "Lagi belajar/kerja"
	// still on youtube for the snooze time (90 min) and cooldown: nothing
	if r.feed(89*time.Minute, true, "youtube.com", false) {
		t.Fatalf("nudged during the snooze: %v", r.fires)
	}
	// another site is not snoozed (but the cooldown is long over)
	if !r.feed(time.Minute, true, "tiktok.com", false) {
		t.Fatal("a different site must still be nudged")
	}
	// after the snooze, the first site can be nudged again
	r.feed(5*time.Minute, false, "", false)
	r.now = r.now.Add(2 * time.Hour)
	if !r.feed(31*time.Minute, true, "youtube.com", false) {
		t.Fatal("no nudge after the snooze ended")
	}
}

func TestSnoozeEndsWithTheDay(t *testing.T) {
	r := newRun(t)
	r.now = time.Date(2026, 11, 25, 22, 30, 0, 0, time.UTC)
	r.n.SnoozeKey("youtube.com", r.now)
	if got := r.n.snoozed["youtube.com"]; !got.Equal(time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("snooze until %v: it must not run past midnight", got)
	}
}

func TestNudgeOffAndAsleep(t *testing.T) {
	r := newRun(t)
	r.enabled = false // /tegur off
	if r.feed(2*time.Hour, true, "youtube.com", false) {
		t.Fatal("nudged while switched off")
	}
	r.enabled = true
	if !r.feed(10*time.Second, true, "youtube.com", false) {
		t.Fatal("switching on again should nudge a streak that is still going")
	}
	// form tidur: no nudge
	r2 := newRun(t)
	r2.n.streak = time.Hour
	if r2.n.Step(NudgeSample{Now: r2.now, Dt: 10 * time.Second, Distract: true, Key: "x.com", Asleep: true, Enabled: true}) {
		t.Fatal("nudged while asleep")
	}
}
