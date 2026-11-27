package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/ardwiinoo/tunas/internal/forms"
)

// ConnState is the agent's connection to the server, shown by the tray icon.
type ConnState int

const (
	ConnConnecting ConnState = iota // starting, or reconnecting after a failure (yellow)
	ConnOnline                      // last long-poll succeeded (green)
	ConnOffline                     // several polls in a row failed (red)
	ConnOnlineWarn                  // online, but something needs attention soon: the mTLS certificate expires (yellow)
)

func (s ConnState) String() string {
	return [...]string{"connecting", "online", "offline", "online_warn"}[s]
}

// offlineAfter consecutive failed polls turn the state red.
const offlineAfter = 3

// connAfter maps the number of consecutive failed polls to a state.
func connAfter(failures int) ConnState {
	switch {
	case failures == 0:
		return ConnOnline
	case failures < offlineAfter:
		return ConnConnecting
	}
	return ConnOffline
}

// SetCertWarning sets a warning shown while the agent is online (the tray dot
// turns yellow). "" clears it.
func (a *Agent) SetCertWarning(w string) {
	a.mu.Lock()
	a.certWarn = w
	conn := a.conn
	a.mu.Unlock()
	if conn == ConnOnline || conn == ConnOnlineWarn {
		a.setConn(ConnOnline) // re-evaluate: yellow with a warning, green without
	}
}

// setConn records the state and tells the tray when it changes.
func (a *Agent) setConn(s ConnState) {
	a.mu.Lock()
	if s == ConnOnline && a.certWarn != "" {
		s = ConnOnlineWarn
	}
	changed := s != a.conn
	a.conn = s
	cb := a.OnConn
	a.mu.Unlock()
	if changed {
		a.Log.Info("connection", "state", s.String())
		if cb != nil {
			cb(s)
		}
	}
}

// Conn returns the current connection state.
func (a *Agent) Conn() ConnState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn
}

// SetPaused pauses or resumes activity tracking (tray menu). Forms and
// reminders keep working; nothing is recorded while paused.
func (a *Agent) SetPaused(p bool) {
	a.mu.Lock()
	a.paused = p
	a.mu.Unlock()
	a.Log.Info("tracking", "paused", p)
}

// Paused reports whether tracking is paused.
func (a *Agent) Paused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// HeartbeatEvery is how often the agent reports to /v1/agent/heartbeat.
const HeartbeatEvery = 60 * time.Second

func (a *Agent) heartbeatLoop(ctx context.Context) {
	failing := false
	for {
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		pending, err := a.API.Heartbeat(hctx, Heartbeat{Version: a.Version, Paused: a.Paused(), Stats: a.statsForHeartbeat()})
		cancel()
		a.noteResult(err)
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			a.Log.Warn("heartbeat failed", "err", err)
			failing = true
		case err == nil:
			if failing {
				a.Log.Info("heartbeat recovered", "pending_events", pending)
			}
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(HeartbeatEvery):
		}
	}
}

// Late reminders (after sleep or a lost connection).
const (
	reminderMaxAge  = 2 * time.Minute // older than this is shown as late
	reminderSkipAge = 6 * time.Hour   // older than this is not shown at all
)

// lateText labels a late reminder: "minum air (tertunda 25 menit)".
func lateText(text string, age time.Duration) string {
	m := int(age.Minutes())
	if m < 60 {
		return fmt.Sprintf("%s (tertunda %d menit)", text, m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%s (tertunda %d jam)", text, m/60)
	}
	return fmt.Sprintf("%s (tertunda %d jam %d menit)", text, m/60, m%60)
}

// queueReminder adds a due reminder; a.mu must be held. Reminders are shown
// one after another in event order, each for the configured duration.
func (a *Agent) queueReminder(id int64, text string, age time.Duration, now time.Time) {
	switch {
	case age > reminderSkipAge:
		a.Log.Info("late reminder skipped", "id", id, "age", age.Round(time.Minute).String())
		return
	case age > reminderMaxAge:
		a.Log.Info("late reminder", "id", id, "age", age.Round(time.Minute).String())
		text = lateText(text, age)
	}
	a.reminders = append(a.reminders, text)
	a.nextReminder(now)
}

// nextReminder moves the next queued reminder on stage once the current one
// has had its time; a.mu must be held.
func (a *Agent) nextReminder(now time.Time) {
	showFor := time.Duration(a.Cfg.Forms.ReminderDisplaySeconds) * time.Second
	if len(a.reminders) == 0 || (!a.remindAt.IsZero() && now.Sub(a.remindAt) < showFor) {
		return
	}
	a.reminder, a.remindAt = a.reminders[0], now
	a.reminders = a.reminders[1:]
}

// IdleFor is how long the owner has been idle at the last sample.
func (a *Agent) IdleFor() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastIdle
}

// StatusLine is the tray's first line: "Tunas · online · sinkron 12 d lalu".
func (a *Agent) StatusLine(now time.Time) string {
	a.mu.Lock()
	conn, last, paused := a.conn, a.lastSync, a.paused
	a.mu.Unlock()
	state := map[ConnState]string{ConnOnline: "online", ConnConnecting: "menyambung…", ConnOffline: "offline", ConnOnlineWarn: "online · sertifikat segera kedaluwarsa"}[conn]
	s := "Tunas · " + state
	if paused {
		s += " · dijeda"
	}
	if !last.IsZero() {
		s += " · sinkron " + shortAgo(now.Sub(last)) + " lalu"
	}
	return s
}

func shortAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d d", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d m", int(d.Minutes()))
	}
	return fmt.Sprintf("%d j", int(d.Hours()))
}

// Worklog bubble rules (PRD 8.4): not when idle ≥ 10 min or asleep (tidur),
// not in fokus unless allowed, not for a stale reminder after a reconnect.
const (
	WorklogIdleLimit = 10 * time.Minute
	WorklogStale     = 30 * time.Minute
)

// ShowWorklogBubble decides whether a worklog.due may pop up on screen
// (Telegram always has the reminder).
func ShowWorklogBubble(e WorklogEvent, idle time.Duration, form forms.Form, inFocusOK bool) bool {
	switch {
	case e.Type != "worklog.due":
		return true // closing events always pass
	case e.Age > WorklogStale, idle >= WorklogIdleLimit, form == forms.Tidur:
		return false
	case form == forms.Fokus:
		return inFocusOK
	}
	return true
}
