package sysstats

import (
	"context"
	"testing"
	"time"
)

// The server's share of the monitoring cost (PRD section 9: <= +2 MB). A
// snapshot is taken on demand and every 5 minutes.
func BenchmarkSnapshot(b *testing.B) {
	c := &Collector{Version: "bench", Start: time.Now(), Now: time.Now, CgroupRoot: b.TempDir()}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = c.Snapshot(ctx)
	}
}
