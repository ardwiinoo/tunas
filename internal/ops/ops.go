// Package ops holds operational jobs: health alerts to the owner (agent not
// seen, repeated LLM failures) and daily data cleanup.
package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Notifier sends a message to the owner.
type Notifier interface {
	NotifyOwner(ctx context.Context, text string) error
}

// Defaults from PRD Phase 5.
const (
	AgentDownAfter      = 24 * time.Hour
	AlertCooldown       = time.Hour
	LLMFailureThreshold = 3
	ActivityRetention   = 180 * 24 * time.Hour
	EventRetention      = 30 * 24 * time.Hour
)

// Monitor runs periodic checks and sends alerts with a cooldown.
type Monitor struct {
	DB     *store.DB
	Notify Notifier
	Now    func() time.Time
	Log    *slog.Logger

	mu         sync.Mutex
	lastAlert  map[string]time.Time
	agentDown  bool // alert sent for the current outage
	llmFails   int
	lastPruned time.Time
}

// NewMonitor creates a monitor.
func NewMonitor(db *store.DB, n Notifier, now func() time.Time, log *slog.Logger) *Monitor {
	return &Monitor{DB: db, Notify: n, Now: now, Log: log, lastAlert: map[string]time.Time{}}
}

// alert sends text unless an alert of the same kind went out within the cooldown.
func (m *Monitor) alert(ctx context.Context, kind, text string) bool {
	m.mu.Lock()
	now := m.Now()
	if last, ok := m.lastAlert[kind]; ok && now.Sub(last) < AlertCooldown {
		m.mu.Unlock()
		return false
	}
	m.lastAlert[kind] = now
	m.mu.Unlock()
	if err := m.Notify.NotifyOwner(ctx, text); err != nil {
		m.Log.Warn("alert not sent", "kind", kind, "err", err)
		return false
	}
	m.Log.Info("alert sent", "kind", kind)
	return true
}

// CheckAgent alerts once per outage when no active device was seen for 24h,
// and sends a recovery note when it comes back.
func (m *Monitor) CheckAgent(ctx context.Context) error {
	devs, err := m.DB.ListDevices(ctx)
	if err != nil {
		return err
	}
	var latest *time.Time
	active := 0
	for _, d := range devs {
		if d.RevokedAt != nil {
			continue
		}
		active++
		if d.LastSeenAt != nil && (latest == nil || d.LastSeenAt.After(*latest)) {
			latest = d.LastSeenAt
		}
	}
	if active == 0 || latest == nil {
		return nil // no agent set up yet: nothing to watch
	}
	down := m.Now().Sub(*latest) > AgentDownAfter
	m.mu.Lock()
	wasDown := m.agentDown
	m.mu.Unlock()
	switch {
	case down && !wasDown:
		if m.alert(ctx, "agent_down", fmt.Sprintf("⚠️ Agen laptop tidak terlihat sejak %s lalu.",
			roundDur(m.Now().Sub(*latest)))) {
			m.mu.Lock()
			m.agentDown = true
			m.mu.Unlock()
		}
	case !down && wasDown:
		m.mu.Lock()
		m.agentDown = false
		m.mu.Unlock()
		_ = m.Notify.NotifyOwner(ctx, "✅ Agen laptop terhubung lagi.")
	}
	return nil
}

// Cleanup deletes old activity (180 days) and old acked events, once a day.
func (m *Monitor) Cleanup(ctx context.Context) error {
	now := m.Now()
	if !m.lastPruned.IsZero() && now.Sub(m.lastPruned) < 24*time.Hour {
		return nil
	}
	a, err := m.DB.PruneActivity(ctx, now.Add(-ActivityRetention))
	if err != nil {
		return err
	}
	e, err := m.DB.PruneEvents(ctx, now.Add(-EventRetention))
	if err != nil {
		return err
	}
	m.lastPruned = now
	m.Log.Info("cleanup done", "activity_rows", a, "event_rows", e)
	return nil
}

// Run performs checks every interval until ctx is done.
func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := m.CheckAgent(ctx); err != nil {
			m.Log.Warn("agent check failed", "err", err)
		}
		if err := m.Cleanup(ctx); err != nil {
			m.Log.Warn("cleanup failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Watch wraps an LLM completer and alerts after LLMFailureThreshold
// consecutive failures (cap/disabled do not count).
func (m *Monitor) Watch(inner llm.Completer) llm.Completer {
	return &watched{inner: inner, m: m}
}

type watched struct {
	inner llm.Completer
	m     *Monitor
}

func (w *watched) Complete(ctx context.Context, msgs []llm.Message) (string, error) {
	out, err := w.inner.Complete(ctx, msgs)
	m := w.m
	m.mu.Lock()
	switch {
	case err == nil:
		m.llmFails = 0
	case errors.Is(err, llm.ErrCap) || errors.Is(err, llm.ErrDisabled) || errors.Is(err, context.Canceled):
	default:
		m.llmFails++
	}
	n := m.llmFails
	m.mu.Unlock()
	if n >= LLMFailureThreshold {
		m.alert(context.WithoutCancel(ctx), "llm_failing",
			fmt.Sprintf("⚠️ AI gagal %d kali berturut-turut (terakhir: %v). Cek Bifrost/model di .env.", n, err))
	}
	return out, err
}

func roundDur(d time.Duration) string {
	h := int(d.Hours())
	if h >= 48 {
		return fmt.Sprintf("%d hari", h/24)
	}
	return fmt.Sprintf("%d jam", h)
}
