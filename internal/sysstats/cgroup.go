// Package sysstats reads resource numbers: this container's cgroup, the Go
// runtime and database, and the VPS snapshot a host cron script writes. It
// only returns numbers and fixed-shape data, never free text, because the
// numbers travel to Telegram and into stats_samples.
package sysstats

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Cgroup is the container's memory and CPU counters.
type Cgroup struct {
	Version  int   // 1 or 2
	MemBytes int64 // current usage
	MemLimit int64 // 0 = unlimited
	CPUUsec  int64 // cumulative CPU time in microseconds
}

// DefaultCgroupRoot is where cgroups are mounted in a container.
const DefaultCgroupRoot = "/sys/fs/cgroup"

// ErrNoCgroup means no cgroup files were found (e.g. running on Windows).
var ErrNoCgroup = errors.New("no cgroup files")

// ReadCgroup reads the cgroup mounted at root. v2 is detected by
// cgroup.controllers; v1 (memory/ and cpuacct/ subtrees) is the fallback.
func ReadCgroup(root string) (Cgroup, error) {
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err == nil {
		return readV2(root)
	}
	if _, err := os.Stat(filepath.Join(root, "memory")); err == nil {
		return readV1(root)
	}
	return Cgroup{}, ErrNoCgroup
}

func readInt(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func readV2(root string) (Cgroup, error) {
	c := Cgroup{Version: 2}
	var err error
	if c.MemBytes, err = readInt(filepath.Join(root, "memory.current")); err != nil {
		return c, fmt.Errorf("memory.current: %w", err)
	}
	if c.MemLimit, err = readInt(filepath.Join(root, "memory.max")); err != nil { // "max" = unlimited = 0
		return c, fmt.Errorf("memory.max: %w", err)
	}
	f, err := os.Open(filepath.Join(root, "cpu.stat"))
	if err != nil {
		return c, fmt.Errorf("cpu.stat: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == "usage_usec" {
			if c.CPUUsec, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64); err != nil {
				return c, fmt.Errorf("cpu.stat usage_usec: %w", err)
			}
			return c, nil
		}
	}
	return c, errors.New("cpu.stat has no usage_usec")
}

// v1 reports "no limit" as a number close to 2^63.
const v1Unlimited = int64(1) << 60

func readV1(root string) (Cgroup, error) {
	c := Cgroup{Version: 1}
	var err error
	if c.MemBytes, err = readInt(filepath.Join(root, "memory", "memory.usage_in_bytes")); err != nil {
		return c, fmt.Errorf("memory.usage_in_bytes: %w", err)
	}
	if c.MemLimit, err = readInt(filepath.Join(root, "memory", "memory.limit_in_bytes")); err != nil {
		return c, fmt.Errorf("memory.limit_in_bytes: %w", err)
	}
	if c.MemLimit >= v1Unlimited {
		c.MemLimit = 0
	}
	for _, dir := range []string{"cpuacct", "cpu,cpuacct", "cpu"} {
		if ns, err := readInt(filepath.Join(root, dir, "cpuacct.usage")); err == nil {
			c.CPUUsec = ns / 1000 // nanoseconds
			return c, nil
		}
	}
	return c, errors.New("cpuacct.usage not found")
}

// CPUMeter turns cumulative CPU time into a percentage of one core.
type CPUMeter struct {
	prevUsec int64
	prevAt   time.Time
}

// Percent returns the CPU use since the previous call (0 on the first).
func (m *CPUMeter) Percent(now time.Time, usec int64) float64 {
	defer func() { m.prevUsec, m.prevAt = usec, now }()
	if m.prevAt.IsZero() || !now.After(m.prevAt) || usec < m.prevUsec {
		return 0
	}
	return 100 * float64(usec-m.prevUsec) / float64(now.Sub(m.prevAt).Microseconds())
}
