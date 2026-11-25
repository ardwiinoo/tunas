package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/status"
)

// AgentOverFor is how long the agent must stay above twice its memory
// target before an alert goes out.
const AgentOverFor = 10 * time.Minute

// Resources gathers the numbers the resource alerts look at (status.Service
// in production, a fake in tests).
type Resources interface {
	Gather(ctx context.Context) (status.Data, error)
	Sample(ctx context.Context) error
}

// CheckResources raises the PRD 8g alerts (same cooldown and delivery as the
// other alerts): server memory, VPS RAM and disk, an unhealthy container,
// agent memory above 2x its target for 10 minutes, daily LLM use. The
// thresholds come from settings keys alerts.*; alerts.enabled=false mutes them.
// It also stores the 5-minute server and host samples.
func (m *Monitor) CheckResources(ctx context.Context) error {
	if m.Res == nil {
		return nil
	}
	if err := m.Res.Sample(ctx); err != nil {
		m.Log.Warn("stats sample failed", "err", err)
	}
	d, err := m.Res.Gather(ctx)
	if err != nil {
		return err
	}
	t := d.T
	if t == (status.Thresholds{}) {
		t = status.DefaultThresholds()
	}
	if !t.Enabled {
		return nil
	}
	now := m.Now()

	if s := d.Server; s.MemLimit > 0 && s.MemPct() > t.ServerMem {
		m.alert(ctx, "res_server_mem", fmt.Sprintf("⚠️ Memori server Tunas %.0f%% dari batas kontainer (ambang %.0f%%). Cek /status vps.", s.MemPct(), t.ServerMem))
	}
	if h := d.Host; h != nil {
		if h.MemPct() > t.VPSRAM {
			m.alert(ctx, "res_vps_ram", fmt.Sprintf("⚠️ RAM VPS %.0f%% terpakai (ambang %.0f%%).", h.MemPct(), t.VPSRAM))
		}
		for _, dk := range h.Disks {
			if float64(dk.UsedPct) > t.Disk {
				m.alert(ctx, "res_disk:"+dk.Mount, fmt.Sprintf("⚠️ Disk %s %d%% terpakai, sisa %.1f GB (ambang %.0f%%).", dk.Mount, dk.UsedPct, dk.FreeGB, t.Disk))
			}
		}
		for _, c := range h.Containers {
			if c.Health == "unhealthy" || (c.Status != "" && c.Status != "running") {
				state := c.Health
				if state != "unhealthy" {
					state = c.Status
				}
				m.alert(ctx, "res_container:"+c.Name, fmt.Sprintf("⚠️ Kontainer %s tidak sehat (%s).", c.Name, state))
			}
		}
	}
	if d.LLMEnabled && d.LLMCap > 0 && d.LLMUsePct() > t.LLM {
		m.alert(ctx, "res_llm", fmt.Sprintf("⚠️ Pemakaian LLM hari ini %d/%d (%.0f%%, ambang %.0f%%).", d.LLMUsed, d.LLMCap, d.LLMUsePct(), t.LLM))
	}

	var over []string
	for _, l := range d.Laptops {
		key := l.Name
		if l.Stats == nil || !l.Online || l.Stats.PrivateMB <= 2*status.AgentMemTarget(l.Stats.Overlay) {
			m.mu.Lock()
			delete(m.agentOver, key)
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		since, ok := m.agentOver[key]
		if !ok {
			m.agentOver[key] = now
			since = now
		}
		m.mu.Unlock()
		if now.Sub(since) >= AgentOverFor {
			over = append(over, key)
			m.alert(ctx, "res_agent_mem:"+key, fmt.Sprintf("⚠️ Memori agen %s %.0f MB, lebih dari 2× target (%.0f MB) selama %d menit.",
				key, l.Stats.PrivateMB, status.AgentMemTarget(l.Stats.Overlay), int(now.Sub(since).Minutes())))
		}
	}
	if len(over) > 0 {
		m.Log.Info("agent memory over target", "devices", strings.Join(over, ","))
	}
	return nil
}
