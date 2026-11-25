package sysstats

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs scripts/host-stats.sh against fixture /proc files and a fake docker,
// then reads the result with the server's own parser.
func TestHostStatsScript(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, tool := range []string{"awk", "df", "cut", "mktemp"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	dir := t.TempDir()
	proc := filepath.Join(dir, "proc")
	write(t, proc, "uptime", "123456.78 98765.43\n")
	write(t, proc, "loadavg", "0.31 0.28 0.25 1/231 4242\n")
	write(t, proc, "cpuinfo", "processor\t: 0\nmodel name\t: x\n\nprocessor\t: 1\nmodel name\t: x\n")
	write(t, proc, "meminfo", "MemTotal:        2048000 kB\nMemFree:          300000 kB\nMemAvailable:     819200 kB\nSwapTotal:       1048576 kB\nSwapFree:        1048576 kB\n")

	// a fake docker: only what the script asks for. The container also "has"
	// secrets, which the script must never read.
	bin := filepath.Join(dir, "docker")
	write(t, dir, "docker", `#!/bin/sh
case "$*" in
  "ps -a --format"*) printf 'tunas|running\nbifrost|running\nmbelbimbel|running\nold-job|exited\n' ;;
  "ps -q") printf 'a1\nb2\nc3\n' ;;
  "inspect --format"*) printf '/tunas|healthy\n/bifrost|\n/mbelbimbel|unhealthy\n' ;;
  "stats --no-stream"*) printf 'tunas|38.2MiB / 128MiB|0.21%%\nbifrost|210MiB / 1.953GiB|1.50%%\nmbelbimbel|160.5MiB / 1.953GiB|0.00%%\n' ;;
  inspect*) printf 'DATABASE_URL=postgres://u:supersecret@h/db TELEGRAM_BOT_TOKEN=supersecret\n' ;;
esac
`)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "host-stats.sh"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "monitor", "host.json")
	cmd := exec.Command(sh, script, out)
	cmd.Env = append(os.Environ(), "PROC_DIR="+proc, "DOCKER_BIN="+bin, "DISK_MOUNTS=/",
		"DATABASE_URL=postgres://u:supersecret@h/db", "TELEGRAM_BOT_TOKEN=supersecret", "TAVILY_API_KEY=tvly-supersecret")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, b)
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecret") || strings.Contains(string(raw), "postgres://") {
		t.Fatalf("host.json must hold no environment or secret:\n%s", raw)
	}
	h, err := ReadHost(out, time.Now())
	if err != nil {
		t.Fatalf("the server cannot read the script's file: %v\n%s", err, raw)
	}
	if h.UptimeS != 123456 || h.CPUs != 2 || h.Load != [3]float64{0.31, 0.28, 0.25} {
		t.Fatalf("basics %+v", h)
	}
	if h.MemTotalMB != 2000 || h.MemAvailMB != 800 || h.MemUsedMB != 1200 || h.SwapTotal != 1024 || h.SwapUsedMB != 0 {
		t.Fatalf("memory %+v", h)
	}
	if len(h.Disks) != 1 || h.Disks[0].Mount != "/" || h.Disks[0].UsedPct < 0 || h.Disks[0].UsedPct > 100 {
		t.Fatalf("disks %+v", h.Disks)
	}
	byName := map[string]HostContainer{}
	for _, c := range h.Containers {
		byName[c.Name] = c
	}
	tu := byName["tunas"]
	if len(h.Containers) != 4 || tu.Status != "running" || tu.Health != "healthy" || tu.MemMB != 38.2 || tu.MemLimitMB != 128 || tu.CPUPct != 0.21 {
		t.Fatalf("tunas container %+v of %d", tu, len(h.Containers))
	}
	if bf := byName["bifrost"]; bf.MemMB != 210 || bf.MemLimitMB != 0 || bf.Health != "" {
		t.Fatalf("a container without a limit shows the host's RAM in docker: %+v", bf)
	}
	if byName["mbelbimbel"].Health != "unhealthy" || byName["old-job"].Status != "exited" {
		t.Fatalf("health and exited containers: %+v", h.Containers)
	}
	if mb := byName["mbelbimbel"].MemMB; mb != 160.5 {
		t.Fatalf("mbelbimbel memory %v", mb)
	}
	if entries, _ := os.ReadDir(filepath.Dir(out)); len(entries) != 1 {
		t.Fatalf("no temp file may stay behind: %v", entries)
	}

	// no docker at all: the host part still works, the container list is empty
	cmd = exec.Command(sh, script, out)
	cmd.Env = append(os.Environ(), "PROC_DIR="+proc, "DOCKER_BIN="+filepath.Join(dir, "tidak-ada"), "DISK_MOUNTS=/")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script without docker failed: %v\n%s", err, b)
	}
	if h, err := ReadHost(out, time.Now()); err != nil || len(h.Containers) != 0 || h.MemTotalMB != 2000 {
		t.Fatalf("without docker: %+v %v", h, err)
	}
}
