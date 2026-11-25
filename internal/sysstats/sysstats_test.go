package sysstats

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupV2(t *testing.T) {
	root := t.TempDir()
	write(t, root, "cgroup.controllers", "cpuset cpu io memory pids\n")
	write(t, root, "memory.current", "39845888\n")
	write(t, root, "memory.max", "134217728\n")
	write(t, root, "cpu.stat", "usage_usec 12500000\nuser_usec 9000000\nsystem_usec 3500000\nnr_periods 0\n")
	cg, err := ReadCgroup(root)
	if err != nil || cg.Version != 2 || cg.MemBytes != 39845888 || cg.MemLimit != 134217728 || cg.CPUUsec != 12500000 {
		t.Fatalf("v2 = %+v %v", cg, err)
	}
	// "max" is no limit
	write(t, root, "memory.max", "max\n")
	if cg, _ := ReadCgroup(root); cg.MemLimit != 0 {
		t.Fatalf("max → %d, want 0 (unlimited)", cg.MemLimit)
	}
	// broken files are errors, not zeros
	write(t, root, "memory.current", "abc\n")
	if _, err := ReadCgroup(root); err == nil {
		t.Fatal("garbage accepted")
	}
	write(t, root, "memory.current", "1\n")
	write(t, root, "cpu.stat", "user_usec 1\n")
	if _, err := ReadCgroup(root); err == nil || !strings.Contains(err.Error(), "usage_usec") {
		t.Fatalf("missing usage_usec: %v", err)
	}
}

func TestCgroupV1(t *testing.T) {
	root := t.TempDir()
	write(t, root, "memory/memory.usage_in_bytes", "52428800\n")
	write(t, root, "memory/memory.limit_in_bytes", "134217728\n")
	write(t, root, "cpuacct/cpuacct.usage", "7500000000\n") // nanoseconds
	cg, err := ReadCgroup(root)
	if err != nil || cg.Version != 1 || cg.MemBytes != 52428800 || cg.MemLimit != 134217728 || cg.CPUUsec != 7500000 {
		t.Fatalf("v1 = %+v %v", cg, err)
	}
	// the "no limit" number of v1
	write(t, root, "memory/memory.limit_in_bytes", "9223372036854771712\n")
	if cg, _ := ReadCgroup(root); cg.MemLimit != 0 {
		t.Fatalf("v1 unlimited → %d", cg.MemLimit)
	}
	// the combined directory name some kernels use
	r2 := t.TempDir()
	write(t, r2, "memory/memory.usage_in_bytes", "1\n")
	write(t, r2, "memory/memory.limit_in_bytes", "2\n")
	write(t, r2, "cpu,cpuacct/cpuacct.usage", "3000\n")
	if cg, err := ReadCgroup(r2); err != nil || cg.CPUUsec != 3 {
		t.Fatalf("cpu,cpuacct: %+v %v", cg, err)
	}
	if _, err := ReadCgroup(t.TempDir()); !errors.Is(err, ErrNoCgroup) {
		t.Fatalf("no cgroup: %v", err)
	}
}

func TestCPUMeter(t *testing.T) {
	var m CPUMeter
	t0 := time.Unix(1000, 0)
	if m.Percent(t0, 1_000_000) != 0 {
		t.Fatal("first call is 0")
	}
	// 0.5 s of CPU in 10 s of wall time = 5% of one core
	if got := m.Percent(t0.Add(10*time.Second), 1_500_000); got < 4.99 || got > 5.01 {
		t.Fatalf("cpu = %v", got)
	}
	if m.Percent(t0.Add(20*time.Second), 100) != 0 {
		t.Fatal("a counter that went back (restart) is 0, not negative")
	}
}

const goodHost = `{"ts":%d,"uptime_s":123456,"load":[0.31,0.28,0.25],"cpus":1,
"mem_total_mb":2000,"mem_used_mb":1200,"mem_avail_mb":800,"swap_total_mb":1024,"swap_used_mb":0,
"disks":[{"mount":"/","used_pct":41,"free_gb":23.5},{"mount":"/srv","used_pct":12,"free_gb":9.1}],
"containers":[{"name":"tunas","status":"running","health":"healthy","mem_mb":38,"mem_limit_mb":128,"cpu_pct":0.2},
{"name":"bifrost","status":"running","health":"","mem_mb":210,"mem_limit_mb":0,"cpu_pct":1.5}]}`

func host(ts time.Time) string { return strings.Replace(goodHost, "%d", itoa(ts.Unix()), 1) }

func itoa(n int64) string {
	b := []byte{}
	if n == 0 {
		return "0"
	}
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestParseHost(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h, err := ParseHost(strings.NewReader(host(now.Add(-2*time.Minute))), now)
	if err != nil || h.CPUs != 1 || len(h.Containers) != 2 || h.Containers[0].Name != "tunas" || h.MemPct() != 60 {
		t.Fatalf("good file: %+v %v", h, err)
	}
	// stale: the cron stopped
	if _, err := ParseHost(strings.NewReader(host(now.Add(-6*time.Minute))), now); !errors.Is(err, ErrHostStale) {
		t.Fatalf("6 minutes old: %v", err)
	}
	if _, err := ParseHost(strings.NewReader(host(now.Add(-5*time.Minute))), now); err != nil {
		t.Fatalf("exactly 5 minutes is still fine: %v", err)
	}
	bad := map[string]string{
		"not json":      "{rusak",
		"empty":         "",
		"trailing":      host(now) + `{"x":1}`,
		"no timestamp":  strings.Replace(host(now), `"ts":`, `"tt":`, 1),
		"bad container": strings.Replace(host(now), `"name":"tunas"`, `"name":"tunas; rm -rf /"`, 1),
		"bad mount":     strings.Replace(host(now), `"mount":"/srv"`, `"mount":"srv"`, 1),
		"pct over 100":  strings.Replace(host(now), `"used_pct":41`, `"used_pct":410`, 1),
		"status text":   strings.Replace(host(now), `"status":"running"`, `"status":"Up 3 days (healthy)"`, 1),
	}
	for name, in := range bad {
		if _, err := ParseHost(strings.NewReader(in), now); !errors.Is(err, ErrHostBad) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The file must not carry environment variables, arguments or anything else
// not in the fixed shape: an extra field makes it invalid.
func TestParseHostRejectsLeaks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, in := range map[string]string{
		"env at top level":  strings.Replace(host(now), `"cpus":1`, `"cpus":1,"env":{"DATABASE_URL":"postgres://u:p@h/db"}`, 1),
		"args in container": strings.Replace(host(now), `"name":"tunas"`, `"name":"tunas","cmd":"/tunas-server serve","env":["TOKEN=x"]`, 1),
		"hostname":          strings.Replace(host(now), `"cpus":1`, `"cpus":1,"hostname":"vps-1"`, 1),
	} {
		if _, err := ParseHost(strings.NewReader(in), now); !errors.Is(err, ErrHostBad) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestParseHostSizeLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	big := host(now) + strings.Repeat(" ", MaxHostFile)
	if _, err := ParseHost(strings.NewReader(big), now); !errors.Is(err, ErrHostBad) {
		t.Fatalf("a file over %d bytes: %v", MaxHostFile, err)
	}
	var many strings.Builder
	many.WriteString(`{"ts":1800000000,"cpus":1,"containers":[`)
	for i := 0; i < 70; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name":"c","status":"running","health":"","mem_mb":1,"mem_limit_mb":0,"cpu_pct":0}`)
	}
	many.WriteString(`]}`)
	if _, err := ParseHost(strings.NewReader(many.String()), now); !errors.Is(err, ErrHostBad) {
		t.Fatalf("70 containers: %v", err)
	}
}

func TestReadHostFile(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if _, err := ReadHost("", now); !errors.Is(err, ErrHostOff) {
		t.Fatalf("no path: %v", err)
	}
	dir := t.TempDir()
	if _, err := ReadHost(filepath.Join(dir, "nope.json"), now); !errors.Is(err, ErrHostEmpty) {
		t.Fatalf("missing file: %v", err)
	}
	write(t, dir, "host.json", host(now.Add(-time.Minute)))
	if h, err := ReadHost(filepath.Join(dir, "host.json"), now); err != nil || h.CPUs != 1 {
		t.Fatalf("read: %+v %v", h, err)
	}
}
