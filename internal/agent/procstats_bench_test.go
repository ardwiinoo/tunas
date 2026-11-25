package agent

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/projects"
)

// The cost of monitoring itself (PRD section 9: agent < 0.1% CPU, <= +1 MB).
// One sample every 30 s: the time and allocations here times 2 per minute is
// the whole bill.
func BenchmarkSampleStats(b *testing.B) {
	a := &Agent{Cfg: testCfg(), Log: slog.New(slog.DiscardHandler), Version: "bench",
		Proc: &fakeProc{s: ProcSample{PrivateBytes: 50 << 20, WorkingSet: 30 << 20}}}
	a.Buf = openBenchBuf(b)
	a.init()
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		a.SampleStats(ctx)
	}
}

func openBenchBuf(b *testing.B) *Buffer {
	b.Helper()
	buf, err := OpenBuffer(b.TempDir() + "/buffer.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { buf.Close() })
	return buf
}

// The cost of project recording on each sample (every 10 s): reading the
// title (a fake here), matching the pattern and one SQLite upsert.
func BenchmarkRecordProject(b *testing.B) {
	ext, err := projects.NewExtractor(projects.DefaultEditors())
	if err != nil {
		b.Fatal(err)
	}
	a := &Agent{Cfg: testCfg(), Log: slog.New(slog.DiscardHandler), Extractor: ext,
		Titles: projects.ReaderFunc(func() (string, string, error) { return "Code.exe", "project-A - Visual Studio Code", nil })}
	a.Buf = openBenchBuf(b)
	a.init()
	a.SetProjects(true)
	ctx := context.Background()
	now := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		a.recordProject(ctx, now, "Code.exe", 10)
	}
}
