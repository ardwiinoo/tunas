package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
)

// ProcSample is the agent process's own resource use. Only this process:
// the agent never lists or inspects other processes for statistics.
type ProcSample struct {
	PrivateBytes uint64        // private bytes: Task Manager's "Memory" column
	WorkingSet   uint64        // working set
	CPUTime      time.Duration // user + kernel time since the process started
}

// ProcReader reads the agent process's counters (Windows API in production,
// a fake in tests).
type ProcReader interface {
	Read() (ProcSample, error)
}

// StatsEvery is how often the agent samples itself; the heartbeat (every
// 60 s) sends the latest sample.
const StatsEvery = 30 * time.Second

// statsState is what the sampler keeps between samples.
type statsState struct {
	mu       sync.Mutex
	started  time.Time
	last     agentstats.Stats
	haveLast bool
	prevCPU  time.Duration
	prevAt   time.Time
	peakPriv float64 // highest private MB since the last heartbeat
	errKind  string  // kind of the last failure ("" = fine)
}

func mb(b uint64) float64 { return float64(b) / (1 << 20) }

// SampleStats takes one sample of the agent itself.
func (a *Agent) SampleStats(ctx context.Context) {
	a.init()
	if a.Proc == nil {
		return
	}
	ps, err := a.Proc.Read()
	if err != nil {
		a.Log.Debug("process stats unavailable", "err", err)
		return
	}
	now := a.Now()
	st := &a.stats
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.started.IsZero() {
		st.started = now
	}
	cpu := 0.0
	if !st.prevAt.IsZero() && now.After(st.prevAt) && ps.CPUTime >= st.prevCPU {
		cpu = 100 * float64(ps.CPUTime-st.prevCPU) / (float64(now.Sub(st.prevAt)) * float64(runtime.NumCPU()))
		cpu = min(cpu, 100)
	}
	st.prevCPU, st.prevAt = ps.CPUTime, now
	priv := mb(ps.PrivateBytes)
	st.peakPriv = max(st.peakPriv, priv)

	a.mu.Lock()
	syncAge := int64(-1)
	if !a.lastSync.IsZero() {
		syncAge = int64(now.Sub(a.lastSync).Seconds())
	}
	paused := a.paused
	a.mu.Unlock()
	pending, _ := a.Buf.CountPending(ctx)
	overlay := ""
	if a.OverlayState != nil {
		overlay = a.OverlayState()
	}
	st.last = agentstats.Stats{
		TS: now.Unix(), Version: a.Version, UptimeS: int64(now.Sub(st.started).Seconds()),
		PrivateMB: priv, PeakPrivMB: st.peakPriv, WorkingMB: mb(ps.WorkingSet), CPUPct: cpu,
		Goroutines: runtime.NumGoroutine(), QueueAct: pending, QueueWL: a.QueuedWorklog(ctx),
		SyncAgeS: syncAge, LastErr: st.errKind, Overlay: overlay, Paused: paused,
	}
	st.haveLast = true
	sample := st.last
	if a.StatsFile != "" {
		if err := WriteStatsFile(a.StatsFile, sample); err != nil {
			a.Log.Debug("stats file not written", "err", err)
		}
	}
}

// Stats returns the latest sample (ok=false before the first one).
func (a *Agent) Stats() (agentstats.Stats, bool) {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()
	return a.stats.last, a.stats.haveLast
}

// statsForHeartbeat is the latest sample, and restarts the peak window.
func (a *Agent) statsForHeartbeat() *agentstats.Stats {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()
	if !a.stats.haveLast {
		return nil
	}
	s := a.stats.last
	a.stats.peakPriv = s.PrivateMB
	return &s
}

func (a *Agent) statsLoop(ctx context.Context) {
	a.SampleStats(ctx)
	t := time.NewTicker(StatsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.SampleStats(ctx)
		}
	}
}

// noteResult records whether a server call worked; the kind (never the text)
// of the last failure travels with the next heartbeat.
func (a *Agent) noteResult(err error) {
	a.stats.mu.Lock()
	a.stats.errKind = errKind(err)
	a.stats.mu.Unlock()
}

// errKind classifies a failure into the few words the statistics may carry.
func errKind(err error) string {
	if err == nil {
		return ""
	}
	var se *StatusError
	var ne net.Error
	switch {
	case errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden):
		return "auth"
	case errors.As(err, &se) && se.Code >= 500:
		return "server"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case errors.As(err, &ne):
		return "network"
	}
	return "other"
}
