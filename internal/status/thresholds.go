package status

import (
	"context"
	"fmt"
)

// Settings keys for the resource alerts (PRD 8g: "settings kunci alerts.*").
const (
	KeyAlertsOn  = "alerts.enabled"
	KeyServerMem = "alerts.server_mem_pct"
	KeyVPSRAM    = "alerts.vps_ram_pct"
	KeyDisk      = "alerts.disk_pct"
	KeyLLM       = "alerts.llm_pct"
)

// ThresholdNames maps the short names of /settings ambang to their keys.
var ThresholdNames = map[string]string{"server": KeyServerMem, "ram": KeyVPSRAM, "disk": KeyDisk, "llm": KeyLLM}

// SettingsStore is the settings table.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string, dst any) (bool, error)
	SetSetting(ctx context.Context, key string, value any) error
}

// Thresholds are the limits that trigger an alert, in percent.
type Thresholds struct {
	Enabled   bool
	ServerMem float64 // of the container memory limit
	VPSRAM    float64
	Disk      float64
	LLM       float64 // of the daily request cap
}

// DefaultThresholds are the PRD values.
func DefaultThresholds() Thresholds {
	return Thresholds{Enabled: true, ServerMem: ServerMemPctMax, VPSRAM: VPSRAMPctMax, Disk: DiskPctMax, LLM: LLMUsePctMax}
}

// LoadThresholds reads the owner's overrides over the defaults.
func LoadThresholds(ctx context.Context, st SettingsStore) (Thresholds, error) {
	t := DefaultThresholds()
	if st == nil {
		return t, nil
	}
	if _, err := st.GetSetting(ctx, KeyAlertsOn, &t.Enabled); err != nil {
		return t, err
	}
	for key, dst := range map[string]*float64{KeyServerMem: &t.ServerMem, KeyVPSRAM: &t.VPSRAM, KeyDisk: &t.Disk, KeyLLM: &t.LLM} {
		if _, err := st.GetSetting(ctx, key, dst); err != nil {
			return t, err
		}
	}
	return t, nil
}

// SetThreshold stores one limit (1..100); reset=true removes the override.
func SetThreshold(ctx context.Context, st SettingsStore, name string, v float64, reset bool) error {
	key, ok := ThresholdNames[name]
	if !ok {
		return fmt.Errorf("unknown threshold %q", name)
	}
	if reset {
		v = DefaultThresholds().byKey(key)
	} else if v < 1 || v > 100 {
		return fmt.Errorf("threshold must be 1..100")
	}
	return st.SetSetting(ctx, key, v)
}

func (t Thresholds) byKey(key string) float64 {
	switch key {
	case KeyServerMem:
		return t.ServerMem
	case KeyVPSRAM:
		return t.VPSRAM
	case KeyDisk:
		return t.Disk
	}
	return t.LLM
}
