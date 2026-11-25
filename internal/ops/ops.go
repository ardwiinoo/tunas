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

	"github.com/ardwiinoo/tunas/internal/agentstatus"
	"github.com/ardwiinoo/tunas/internal/llm"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Notifier sends a message to the owner.
type Notifier interface {
	NotifyOwner(ctx context.Context, text string) error
}

// Defaults from PRD Phase 5.
const (
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

	Loc *time.Location // for times in messages
	Res Resources      // resource alerts and samples (nil = off)

	mu         sync.Mutex
	lastAlert  map[string]time.Time
	devDown    map[int64]bool // offline notice sent for the device's current outage
	llmFails   int
	lastPruned time.Time
	agentOver  map[string]time.Time // device → since when its memory is above 2x target
}

// NewMonitor creates a monitor.
func NewMonitor(db *store.DB, n Notifier, now func() time.Time, loc *time.Location, log *slog.Logger) *Monitor {
	return &Monitor{DB: db, Notify: n, Now: now, Loc: loc, Log: log,
		lastAlert: map[string]time.Time{}, devDown: map[int64]bool{}, agentOver: map[string]time.Time{}}
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

// CheckAgent sends "<device> offline sejak HH:MM" once a device has not been
// seen for agentstatus.NoticeAfter, and "<device> kembali online" when it
// returns (only if the offline notice went out). Each notice kind is sent at
// most once per AlertCooldown per device, and none at all when switched off
// with /settings.
func (m *Monitor) CheckAgent(ctx context.Context) error {
	devs, err := m.DB.ListDevices(ctx)
	if err != nil {
		return err
	}
	now := m.Now()
	for _, d := range devs {
		if d.RevokedAt != nil || d.LastSeenAt == nil {
			continue // revoked, or never connected: nothing to watch
		}
		offline := now.Sub(*d.LastSeenAt) > agentstatus.NoticeAfter
		m.mu.Lock()
		wasDown := m.devDown[d.ID]
		m.mu.Unlock()
		if offline == wasDown {
			continue
		}
		on, err := agentstatus.NotifyEnabled(ctx, m.DB)
		if err != nil {
			return err
		}
		name := agentstatus.Name(d)
		if offline {
			if !on {
				continue
			}
			text := fmt.Sprintf("💤 %s offline sejak %s.", name, agentstatus.Clock(*d.LastSeenAt, now, m.Loc))
			if m.alert(ctx, fmt.Sprintf("agent_offline:%d", d.ID), text) {
				m.setDown(d.ID, true)
			}
			continue
		}
		m.setDown(d.ID, false)
		if on {
			m.alert(ctx, fmt.Sprintf("agent_online:%d", d.ID), fmt.Sprintf("✅ %s kembali online.", name))
		}
	}
	return nil
}

func (m *Monitor) setDown(id int64, down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if down {
		m.devDown[id] = true
	} else {
		delete(m.devDown, id)
	}
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
	st, err := m.DB.PruneStats(ctx, now.Add(-store.StatsRetention))
	if err != nil {
		return err
	}
	pr, err := m.DB.PruneProjectBuckets(ctx, now.Add(-ActivityRetention))
	if err != nil {
		return err
	}
	m.lastPruned = now
	m.Log.Info("cleanup done", "activity_rows", a, "event_rows", e, "stats_rows", st, "project_rows", pr)
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
		if err := m.CheckResources(ctx); err != nil {
			m.Log.Warn("resource check failed", "err", err)
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

// ModelBlocked alerts the owner that the gateway refused a model (401/403/
// 404). That is a configuration problem (virtual key permissions or a wrong
// model name), so it is reported even when a fallback answered. At most once
// per AlertCooldown per model; sent in the background so the chat is not
// held up.
func (m *Monitor) ModelBlocked(model string, status int) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.alert(ctx, "llm_blocked:"+model, fmt.Sprintf(
			"⚠️ Gateway menolak model %s (HTTP %d). Ini masalah konfigurasi: cek izin virtual key Bifrost "+
				"untuk model itu, atau nama model di LLM_PRIMARY_MODEL / LLM_FALLBACK_MODELS.", model, status))
	}()
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
