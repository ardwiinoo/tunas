package sysstats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

// Limits for the VPS snapshot file.
const (
	MaxHostFile = 64 << 10 // the file is tiny; anything bigger is not ours
	HostMaxAge  = 5 * time.Minute
)

// Errors for host.json.
var (
	ErrHostOff   = errors.New("host stats off")        // HOST_STATS_FILE empty
	ErrHostStale = errors.New("host stats stale")      // older than HostMaxAge: check the cron
	ErrHostBad   = errors.New("host stats unreadable") // missing, too big, wrong shape
	ErrHostEmpty = errors.New("host stats file missing")
)

// Host is the VPS snapshot written by scripts/host-stats.sh. Only numbers
// and names: no environment, no arguments, no secrets. Unknown fields make
// the file invalid, so a script that starts to leak more is noticed.
type Host struct {
	TS         int64           `json:"ts"` // unix seconds
	UptimeS    int64           `json:"uptime_s"`
	Load       [3]float64      `json:"load"`
	CPUs       int             `json:"cpus"`
	MemTotalMB int64           `json:"mem_total_mb"`
	MemUsedMB  int64           `json:"mem_used_mb"`
	MemAvailMB int64           `json:"mem_avail_mb"`
	SwapTotal  int64           `json:"swap_total_mb"`
	SwapUsedMB int64           `json:"swap_used_mb"`
	Disks      []HostDisk      `json:"disks"`
	Containers []HostContainer `json:"containers"`
}

// HostDisk is one mount.
type HostDisk struct {
	Mount   string  `json:"mount"`
	UsedPct int     `json:"used_pct"`
	FreeGB  float64 `json:"free_gb"`
}

// HostContainer is one Docker container.
type HostContainer struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"` // running | exited | restarting | ...
	Health     string  `json:"health"` // healthy | unhealthy | starting | "" (no healthcheck)
	MemMB      float64 `json:"mem_mb"`
	MemLimitMB float64 `json:"mem_limit_mb"` // 0 = no limit
	CPUPct     float64 `json:"cpu_pct"`
}

// MemPct is the used share of RAM (available memory counted as free).
func (h Host) MemPct() float64 {
	if h.MemTotalMB <= 0 {
		return 0
	}
	return 100 * float64(h.MemTotalMB-h.MemAvailMB) / float64(h.MemTotalMB)
}

var reName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var reWord = regexp.MustCompile(`^[a-z-]{0,20}$`)
var reMount = regexp.MustCompile(`^/[A-Za-z0-9_./-]{0,40}$`)

// ParseHost reads and validates a snapshot. now is the clock for staleness.
func ParseHost(r io.Reader, now time.Time) (Host, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxHostFile+1))
	if err != nil {
		return Host{}, err
	}
	if len(raw) > MaxHostFile {
		return Host{}, fmt.Errorf("%w: larger than %d bytes", ErrHostBad, MaxHostFile)
	}
	var h Host
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return Host{}, fmt.Errorf("%w: %v", ErrHostBad, err)
	}
	if dec.More() {
		return Host{}, fmt.Errorf("%w: trailing data", ErrHostBad)
	}
	if h.TS <= 0 || h.CPUs < 0 || h.MemTotalMB < 0 || len(h.Containers) > 64 || len(h.Disks) > 16 {
		return Host{}, fmt.Errorf("%w: implausible values", ErrHostBad)
	}
	for _, d := range h.Disks {
		if !reMount.MatchString(d.Mount) || d.UsedPct < 0 || d.UsedPct > 100 {
			return Host{}, fmt.Errorf("%w: bad disk entry", ErrHostBad)
		}
	}
	for _, c := range h.Containers {
		if !reName.MatchString(c.Name) || !reWord.MatchString(c.Status) || !reWord.MatchString(c.Health) {
			return Host{}, fmt.Errorf("%w: bad container entry", ErrHostBad)
		}
	}
	if age := now.Sub(time.Unix(h.TS, 0)); age > HostMaxAge {
		return h, fmt.Errorf("%w (%s old)", ErrHostStale, age.Round(time.Second))
	}
	return h, nil
}

// ReadHost reads path; "" means the feature is off.
func ReadHost(path string, now time.Time) (Host, error) {
	if path == "" {
		return Host{}, ErrHostOff
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Host{}, ErrHostEmpty
		}
		return Host{}, fmt.Errorf("%w: %v", ErrHostBad, err)
	}
	defer f.Close()
	return ParseHost(f, now)
}
