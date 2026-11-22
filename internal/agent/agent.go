package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/forms"
)

// API is the subset of the server client the agent uses (faked in tests).
type API interface {
	PostActivity(ctx context.Context, bs []Bucket) error
	Events(ctx context.Context, after int64, wait time.Duration) ([]Event, error)
	Ack(ctx context.Context, upTo int64) error
	State(ctx context.Context) (State, error)
}

const (
	syncBatch      = 500
	maxBackoff     = 15 * time.Minute
	eventWait      = 25 * time.Second
	reminderMaxAge = 2 * time.Minute
	kvLastEventID  = "last_event_id"
)

// Agent ties sampling, buffering, sync and the form machine together.
type Agent struct {
	Cfg     *config.Agent
	Sampler Sampler
	Buf     *Buffer
	API     API
	Log     *slog.Logger
	Now     func() time.Time
	// OnForm is called when the computed form or speech changes (overlay hook).
	OnForm func(out forms.Output)

	machine *forms.Machine
	streak  forms.Streak
	last    forms.Output

	mu       sync.Mutex // guards the fields below (set by event handling)
	override forms.Form
	skew     time.Duration // server time - local time
	reminder string
	remindAt time.Time
	doneAt   time.Time
}

// FormsConfig converts YAML thresholds to forms.Config.
func FormsConfig(c config.AgentForms) forms.Config {
	m, s := time.Minute, time.Second
	return forms.Config{
		TegurAfter:     time.Duration(c.TegurAfterMinutes) * m,
		TegurCooldown:  time.Duration(c.TegurCooldownMinutes) * m,
		TegurFor:       time.Duration(c.TegurDisplaySeconds) * s,
		TidurAfterIdle: time.Duration(c.TidurAfterIdleMinutes) * m,
		SenangFor:      time.Duration(c.SenangSeconds) * s,
		ReminderFor:    time.Duration(c.ReminderDisplaySeconds) * s,
		IdleMotionMin:  time.Duration(c.IdleMotionMinMinutes) * m,
		IdleMotionMax:  time.Duration(c.IdleMotionMaxMinutes) * m,
	}
}

func (a *Agent) init() {
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.machine == nil {
		a.machine = forms.New(FormsConfig(a.Cfg.Forms), nil)
	}
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.init()
	var wg sync.WaitGroup
	wg.Go(func() { a.syncLoop(ctx) })
	wg.Go(func() { a.eventLoop(ctx) })

	interval := time.Duration(a.Cfg.SampleIntervalSeconds) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			// final flush so the last minutes are not lost on clean shutdown
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = a.flush(fctx)
			cancel()
			return nil
		case <-t.C:
			if err := a.SampleOnce(ctx, interval); err != nil && err.Error() != lastErr {
				a.Log.Warn("sample failed", "err", err)
				lastErr = err.Error()
			} else if err == nil {
				lastErr = ""
			}
		}
	}
}

// SampleOnce takes one sample, records it and updates the form.
func (a *Agent) SampleOnce(ctx context.Context, interval time.Duration) error {
	a.init()
	now := a.Now()
	s, err := a.Sampler.Sample()
	if err != nil {
		return err
	}
	idle := s.Idle >= time.Duration(a.Cfg.IdleThresholdSeconds)*time.Second
	cat := "other"
	if s.App != "" {
		cat = a.Cfg.CategoryOf(s.App)
		secs := int(interval.Seconds())
		act, idl := secs, 0
		if idle {
			act, idl = 0, secs
		}
		if err := a.Buf.Add(ctx, now, s.App, cat, act, idl); err != nil {
			return err
		}
	}
	streak := a.streak.Add(cat, idle, interval)

	a.mu.Lock()
	sig := forms.Signals{
		Now: now, Category: cat, Idle: s.Idle, DistractStreak: streak, Override: a.override,
		ReminderText: a.reminder, ReminderAt: a.remindAt, TaskDoneAt: a.doneAt,
	}
	a.mu.Unlock()
	a.applyForm(a.machine.Step(sig))
	return nil
}

func (a *Agent) applyForm(out forms.Output) {
	changed := out.Form != a.last.Form || out.Speech != a.last.Speech
	if changed {
		a.Log.Info("form changed", "from", string(a.last.Form), "to", string(out.Form), "speech", out.Speech)
	}
	if out.Motion {
		a.Log.Debug("idle motion")
	}
	a.last = out
	if (changed || out.Motion) && a.OnForm != nil {
		a.OnForm(out)
	}
}

// CurrentForm returns the last computed output.
func (a *Agent) CurrentForm() forms.Output { return a.last }

// flush uploads pending buckets in batches. Returns the number uploaded.
func (a *Agent) flush(ctx context.Context) (int, error) {
	total := 0
	for {
		bs, err := a.Buf.Pending(ctx, syncBatch)
		if err != nil || len(bs) == 0 {
			return total, err
		}
		if err := a.API.PostActivity(ctx, bs); err != nil {
			return total, err
		}
		if err := a.Buf.MarkSynced(ctx, bs); err != nil {
			return total, err
		}
		total += len(bs)
		// keep the current minute (still accumulating); drop complete synced minutes
		if _, err := a.Buf.Prune(ctx, a.Now().Truncate(time.Minute).Add(-time.Minute)); err != nil {
			return total, err
		}
		if len(bs) < syncBatch {
			return total, nil
		}
	}
}

func (a *Agent) syncLoop(ctx context.Context) {
	base := time.Duration(a.Cfg.FlushIntervalSeconds) * time.Second
	wait := base
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		n, err := a.flush(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait = backoff(base, failures)
			a.Log.Warn("sync failed", "err", err, "retry_in", wait.String())
			continue
		}
		if failures > 0 {
			a.Log.Info("sync recovered", "uploaded", n)
		}
		failures, wait = 0, base
		a.Log.Debug("synced", "uploaded", n)
	}
}

func backoff(base time.Duration, failures int) time.Duration {
	d := base
	for i := 1; i < failures && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

func (a *Agent) eventLoop(ctx context.Context) {
	after := a.loadLastEventID(ctx)
	failures := 0
	sleep := func() bool {
		failures++
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff(5*time.Second, failures)):
			return true
		}
	}
	// initial state (override + clock skew)
	for {
		st, err := a.API.State(ctx)
		if err == nil {
			a.mu.Lock()
			a.skew = st.Now.Sub(a.Now())
			if st.FormOverride != nil {
				a.override = forms.Form(*st.FormOverride)
			}
			a.mu.Unlock()
			failures = 0
			break
		}
		a.Log.Warn("get state failed", "err", err)
		if !sleep() {
			return
		}
	}
	for ctx.Err() == nil {
		evs, err := a.API.Events(ctx, after, eventWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var se *StatusError
			if errors.As(err, &se) && se.Code == 401 {
				a.Log.Error("device token rejected by server (revoked or wrong)")
			} else {
				a.Log.Warn("events poll failed", "err", err)
			}
			if !sleep() {
				return
			}
			continue
		}
		failures = 0
		if len(evs) == 0 {
			continue
		}
		for _, e := range evs {
			a.HandleEvent(e)
			after = max(after, e.ID)
		}
		if err := a.API.Ack(ctx, after); err != nil {
			a.Log.Warn("ack failed", "err", err)
		}
		_ = a.Buf.SetKV(ctx, kvLastEventID, strconv.FormatInt(after, 10))
	}
}

func (a *Agent) loadLastEventID(ctx context.Context) int64 {
	v, _ := a.Buf.GetKV(ctx, kvLastEventID)
	id, _ := strconv.ParseInt(v, 10, 64)
	return id
}

// HandleEvent applies a server event to the agent's signals.
func (a *Agent) HandleEvent(e Event) {
	a.init()
	now := a.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	age := now.Add(a.skew).Sub(e.CreatedAt)
	var p struct {
		Text  string `json:"text"`
		Title string `json:"title"`
		Form  string `json:"form"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	switch e.Type {
	case "reminder.due":
		a.Log.Info("event reminder.due", "id", e.ID, "text", p.Text, "age", age.Round(time.Second).String())
		if age <= reminderMaxAge {
			a.reminder, a.remindAt = p.Text, now
		}
	case "task.done":
		a.Log.Info("event task.done", "id", e.ID)
		if age <= time.Duration(a.Cfg.Forms.TaskDoneWindowSeconds)*time.Second {
			a.doneAt = now
		}
	case "task.created":
		a.Log.Info("event task.created", "id", e.ID)
	case "form.set":
		a.Log.Info("event form.set", "id", e.ID, "form", p.Form)
		if p.Form == "" || p.Form == "auto" {
			a.override = ""
		} else if forms.Valid(forms.Form(p.Form)) {
			a.override = forms.Form(p.Form)
		}
	default:
		a.Log.Debug("unknown event", "type", e.Type)
	}
}

// ErrAlreadyRunning is returned by SingleInstance when another agent holds the lock.
var ErrAlreadyRunning = errors.New("another tunas-agent is already running")
