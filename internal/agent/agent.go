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
	"github.com/ardwiinoo/tunas/internal/projects"
)

// API is the subset of the server client the agent uses (faked in tests).
type API interface {
	PostActivity(ctx context.Context, bs []Bucket) error
	Events(ctx context.Context, after int64, wait time.Duration) ([]Event, error)
	Ack(ctx context.Context, upTo int64) error
	State(ctx context.Context) (State, error)
	Heartbeat(ctx context.Context, hb Heartbeat) (int, error)
}

const (
	syncBatch     = 500
	maxBackoff    = 15 * time.Minute
	eventWait     = 25 * time.Second
	kvLastEventID = "last_event_id"
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
	// Proc reads the agent's own memory and CPU (statistics for /status).
	Proc ProcReader
	// OverlayState reports "off", "hidden" or "visible" (statistics).
	OverlayState func() string
	// StatsFile, when set, receives every sample (for `tunas-agent.exe --status`).
	StatsFile string
	// CategoriesFile keeps the last app → category mapping from the server.
	CategoriesFile string
	// Extractor and Titles read the project name of an allowed editor
	// (Fase 8f). Both nil = project recording unavailable.
	Extractor *projects.Extractor
	Titles    projects.TitleReader
	// OnConn is called when the connection state changes (tray icon).
	OnConn func(ConnState)
	// OnNudge shows the distraction nudge (a question with two answers).
	OnNudge func(text string)
	// Version is reported in the heartbeat.
	Version string
	// RetryBase is the first retry delay of the event loop (default 5s).
	RetryBase time.Duration
	// OnWorklogQueue reports how many worklog entries wait to be sent (tray).
	OnWorklogQueue func(n int)
	// OnWorklog receives worklog.due / worklog.closed events (bubble).
	OnWorklog func(e WorklogEvent)

	machine *forms.Machine
	last    forms.Output

	mu        sync.Mutex // guards the fields below (set by event handling)
	override  forms.Form
	skew      time.Duration // server time - local time
	reminder  string
	remindAt  time.Time
	reminders []string // due reminders waiting for their turn
	doneAt    time.Time
	conn      ConnState
	paused    bool

	worklogKick chan struct{}
	stats       statsState
	cats        categoryState
	nudge       nudgeState
	proj        projectState
	dom         domainState
	lastIdle    time.Duration // idle time at the last sample
	lastSync    time.Time     // last successful upload (tray status)
}

// WorklogEvent is a worklog reminder event from the server.
type WorklogEvent struct {
	Type     string // "worklog.due" | "worklog.closed"
	PromptID int64
	Slot     string
	Count    int
	Via      string
	Age      time.Duration
}

// NudgeConfig converts the YAML thresholds of the distraction nudge.
func NudgeConfig(c config.AgentForms) forms.NudgeConfig {
	m := time.Minute
	return forms.NudgeConfig{After: time.Duration(c.DistractAfter()) * m, Gap: 2 * m,
		Snooze: time.Duration(c.DistractSnoozeMinutes) * m, Cooldown: time.Duration(c.DistractCooldown()) * m, MaxPerDay: c.DistractMaxPerDay}
}

// FormsConfig converts YAML thresholds to forms.Config.
func FormsConfig(c config.AgentForms) forms.Config {
	m, s := time.Minute, time.Second
	return forms.Config{
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
	if a.RetryBase <= 0 {
		a.RetryBase = 5 * time.Second
	}
	if a.worklogKick == nil {
		a.worklogKick = make(chan struct{}, 1)
	}
}

// Run blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.init()
	var wg sync.WaitGroup
	wg.Go(func() { a.syncLoop(ctx) })
	wg.Go(func() { a.eventLoop(ctx) })
	wg.Go(func() { a.heartbeatLoop(ctx) })
	wg.Go(func() { a.worklogLoop(ctx) })
	wg.Go(func() { a.statsLoop(ctx) })
	wg.Go(func() { a.categoriesLoop(ctx) })
	a.queueChanged(ctx)

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
	cat, key, host := "other", s.App, ""
	a.mu.Lock()
	a.lastIdle = s.Idle
	a.mu.Unlock()
	if s.App != "" && !a.Paused() {
		cat = a.categoryOf(s.App)
		secs := int(interval.Seconds())
		act, idl := secs, 0
		if idle {
			act, idl = 0, secs
		}
		if err := a.Buf.Add(ctx, now, s.App, cat, act, idl); err != nil {
			return err
		}
		if !idle {
			a.recordProject(ctx, now, s.App, act)
			host = a.recordDomain(ctx, now, s.App, act)
		}
	}
	// the form and the nudge follow the website when the browser shows one that has a category
	if host != "" {
		if dc := a.domainCategory(host); dc != "" {
			cat, key = dc, host
		}
	}
	if !a.Paused() {
		a.stepNudge(now, interval, cat, key, idle)
	}

	a.mu.Lock()
	a.nextReminder(now)
	sig := forms.Signals{
		Now: now, Category: cat, Idle: s.Idle, Override: a.override,
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

// flush uploads pending activity and project buckets. Returns the number of
// activity buckets uploaded.
func (a *Agent) flush(ctx context.Context) (int, error) {
	n, err := a.flushActivity(ctx)
	if err != nil {
		return n, err
	}
	if err := a.flushProjects(ctx); err != nil {
		return n, err
	}
	return n, a.flushDomains(ctx)
}

func (a *Agent) flushActivity(ctx context.Context) (int, error) {
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
			a.noteResult(err)
			continue
		}
		a.noteResult(nil)
		if failures > 0 {
			a.Log.Info("sync recovered", "uploaded", n)
		}
		a.mu.Lock()
		a.lastSync = a.Now()
		a.mu.Unlock()
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

// eventLoop connects (GET /state) and then long-polls events until an error,
// after which it backs off and connects again. Connecting first means the
// tray turns green as soon as the server answers, instead of after the first
// long-poll returns (up to eventWait when nothing is pending), and the form
// override and clock skew are refreshed after sleep.
func (a *Agent) eventLoop(ctx context.Context) {
	a.init()
	after := a.loadLastEventID(ctx)
	failures := 0
	var downSince time.Time // first failure of the current outage
	fail := func() bool {
		if failures == 0 {
			downSince = a.Now()
		}
		failures++
		a.setConn(connAfter(failures))
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff(a.RetryBase, failures)):
			return true
		}
	}
	a.setConn(ConnConnecting)
	for ctx.Err() == nil {
		st, err := a.API.State(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.logPollErr("get state failed", err, failures)
			if !fail() {
				return
			}
			continue
		}
		a.mu.Lock()
		a.skew = st.Now.Sub(a.Now())
		if st.FormOverride != nil {
			a.override = forms.Form(*st.FormOverride)
		} else {
			a.override = ""
		}
		a.mu.Unlock()
		var offlineFor time.Duration
		if failures > 0 {
			offlineFor = a.Now().Sub(downSince)
		}
		failures = 0
		a.setConn(ConnOnline)
		a.kickWorklog() // back online: send what was written offline

		for first := true; ctx.Err() == nil; first = false {
			evs, err := a.API.Events(ctx, after, eventWait)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				a.logPollErr("events poll failed", err, failures)
				if !fail() {
					return
				}
				break // connect again
			}
			if first {
				// whatever piled up on the server while away arrives here
				attrs := []any{"pending_events", len(evs)}
				if offlineFor > 0 {
					attrs = append(attrs, "offline_for", offlineFor.Round(time.Second).String())
				}
				a.Log.Info("events connected", attrs...)
			}
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
}

func (a *Agent) logPollErr(msg string, err error, failures int) {
	var se *StatusError
	if errors.As(err, &se) && se.Code == 401 {
		a.Log.Error("device token rejected by server (revoked or wrong)")
		return
	}
	a.Log.Warn(msg, "err", err, "failures", failures+1)
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
		a.queueReminder(e.ID, p.Text, age, now)
	case "task.done":
		a.Log.Info("event task.done", "id", e.ID)
		if age <= time.Duration(a.Cfg.Forms.TaskDoneWindowSeconds)*time.Second {
			a.doneAt = now
		}
	case "task.created":
		a.Log.Info("event task.created", "id", e.ID)
	case "worklog.due", "worklog.closed":
		var w struct {
			PromptID int64  `json:"prompt_id"`
			Slot     string `json:"slot"`
			Count    int    `json:"count_today"`
			Via      string `json:"via"`
		}
		_ = json.Unmarshal(e.Payload, &w)
		a.Log.Info("event "+e.Type, "id", e.ID, "prompt", w.PromptID, "age", age.Round(time.Second).String())
		if cb := a.OnWorklog; cb != nil {
			ev := WorklogEvent{Type: e.Type, PromptID: w.PromptID, Slot: w.Slot, Count: w.Count, Via: w.Via, Age: age}
			go cb(ev) // never block event handling (it holds a.mu)
		}
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
