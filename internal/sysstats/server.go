package sysstats

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// Server is this process's own resource use (the Tunas container).
type Server struct {
	TS         int64             `json:"ts"`
	UptimeS    int64             `json:"uptime_s"`
	Version    string            `json:"version"`
	CgroupOK   bool              `json:"cgroup_ok"`
	MemBytes   int64             `json:"mem_bytes"`  // container usage (cgroup) or the Go runtime's Sys
	MemLimit   int64             `json:"mem_limit"`  // 0 = unlimited
	CPUPct     float64           `json:"cpu_pct"`    // of one core
	GoHeapMB   float64           `json:"go_heap_mb"` // runtime.MemStats.HeapAlloc
	GoSysMB    float64           `json:"go_sys_mb"`  // runtime.MemStats.Sys
	Goroutines int               `json:"goroutines"`
	PoolAcq    int32             `json:"pool_acquired"`
	PoolTotal  int32             `json:"pool_total"`
	PoolMax    int32             `json:"pool_max"`
	DBBytes    int64             `json:"db_bytes"`
	Tables     []store.TableSize `json:"tables,omitempty"` // the five biggest
}

// MemPct is the share of the memory limit in use (0 without a limit).
func (s Server) MemPct() float64 {
	if s.MemLimit <= 0 {
		return 0
	}
	return 100 * float64(s.MemBytes) / float64(s.MemLimit)
}

// DB is what the collector needs from the database.
type DB interface {
	DBSize(ctx context.Context) (int64, error)
	BiggestTables(ctx context.Context, n int) ([]store.TableSize, error)
	PoolStats() (acquired, total, max int32)
}

// Collector gathers Server stats on demand. The database part is cached for
// a minute so /status taps do not hit Postgres each time.
type Collector struct {
	DB         DB // nil: no database numbers
	CgroupRoot string
	Version    string
	Start      time.Time
	Now        func() time.Time

	mu       sync.Mutex
	cpu      CPUMeter
	dbAt     time.Time
	dbBytes  int64
	dbTables []store.TableSize
}

const dbCacheFor = time.Minute

// Snapshot reads the numbers. A missing cgroup (not in a container) falls
// back to the Go runtime's memory.
func (c *Collector) Snapshot(ctx context.Context) Server {
	now := c.Now()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s := Server{
		TS: now.Unix(), UptimeS: int64(now.Sub(c.Start).Seconds()), Version: c.Version,
		GoHeapMB: float64(ms.HeapAlloc) / (1 << 20), GoSysMB: float64(ms.Sys) / (1 << 20),
		Goroutines: runtime.NumGoroutine(), MemBytes: int64(ms.Sys),
	}
	root := c.CgroupRoot
	if root == "" {
		root = DefaultCgroupRoot
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cg, err := ReadCgroup(root); err == nil {
		s.CgroupOK, s.MemBytes, s.MemLimit = true, cg.MemBytes, cg.MemLimit
		s.CPUPct = c.cpu.Percent(now, cg.CPUUsec)
	}
	if c.DB != nil {
		s.PoolAcq, s.PoolTotal, s.PoolMax = c.DB.PoolStats()
		if now.Sub(c.dbAt) >= dbCacheFor {
			if n, err := c.DB.DBSize(ctx); err == nil {
				c.dbBytes = n
			}
			if t, err := c.DB.BiggestTables(ctx, 5); err == nil {
				c.dbTables = t
			}
			c.dbAt = now
		}
		s.DBBytes, s.Tables = c.dbBytes, c.dbTables
	}
	return s
}
