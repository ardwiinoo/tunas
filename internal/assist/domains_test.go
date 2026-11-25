package assist

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/domaincmd"
	"github.com/ardwiinoo/tunas/internal/store"
)

func seedDomains(t *testing.T, db *store.DB, day time.Time) {
	t.Helper()
	ctx := context.Background()
	d, _, err := db.CreateDevice(ctx, "laptop3")
	if err != nil {
		t.Fatal(err)
	}
	var bs []store.DomainBucket
	for i := 0; i < 45; i++ {
		bs = append(bs, store.DomainBucket{Start: day.Add(time.Duration(i) * time.Minute), Domain: "youtube.com", ActiveSeconds: 60})
	}
	for i := 0; i < 20; i++ {
		bs = append(bs, store.DomainBucket{Start: day.Add(time.Duration(60+i) * time.Minute), Domain: "github.com", ActiveSeconds: 60})
	}
	if _, err := db.UpsertDomainBuckets(ctx, d.ID, bs); err != nil {
		t.Fatal(err)
	}
}

// Website names stay off the LLM unless the owner allows it.
func TestTopDomainsStayOffTheLLMByDefault(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"template":"top_domains","range":"this_week"}`, "Paling lama di youtube.com."}}
	a, db, _ := setup(t, f)
	day := time.Date(2026, 11, 23, 8, 0, 0, 0, jkt)
	seedActivity(t, db, day)
	seedDomains(t, db, day)

	got := a.Ask(context.Background(), "minggu ini aku paling lama di situs apa?")
	if !strings.Contains(got, "1. youtube.com — 45 m") || !strings.Contains(got, "2. github.com — 20 m") {
		t.Fatalf("answer = %q", got)
	}
	if len(f.seen) != 1 {
		t.Fatalf("%d LLM calls, want 1 (only the template choice)", len(f.seen))
	}
	if all := f.allContent(); strings.Contains(all, "youtube.com") || strings.Contains(all, "github.com") {
		t.Fatalf("a site name reached the LLM: %q", all)
	}
	if err := db.SetSetting(context.Background(), domaincmd.KeyIncludeLLM, true); err != nil {
		t.Fatal(err)
	}
	f.replies = []string{`{"template":"top_domains","range":"this_week"}`, "Kamu paling lama di youtube.com."}
	if got := a.Ask(context.Background(), "minggu ini aku paling lama di situs apa?"); !strings.Contains(got, "Kamu paling lama di youtube.com") || !strings.Contains(f.allContent(), "1. youtube.com") {
		t.Fatalf("allowed: %q", got)
	}
}

// The daily summary lists the top sites; the LLM only sees them when allowed.
func TestDailySummaryKeepsSitesAwayFromTheLLM(t *testing.T) {
	f := &fakeLLM{replies: []string{"Hari ini produktif dan rapi."}}
	a, db, now := setup(t, f)
	day := time.Date(2026, 11, 23, 8, 0, 0, 0, jkt)
	*now = day.Add(13 * time.Hour)
	seedActivity(t, db, day)
	seedDomains(t, db, day)

	text, ok, err := a.DailySummary(context.Background())
	if err != nil || !ok {
		t.Fatalf("summary %v %v", ok, err)
	}
	if !strings.Contains(text, "Situs teratas:\n1. youtube.com — 45m") || !strings.Contains(text, "Hari ini produktif") {
		t.Fatalf("summary:\n%s", text)
	}
	if all := f.allContent(); strings.Contains(all, "youtube.com") {
		t.Fatalf("a site name reached the LLM through the daily summary: %q", all)
	}

	_ = db.SetSetting(context.Background(), domaincmd.KeyIncludeLLM, true)
	f2 := &fakeLLM{replies: []string{"Banyak di youtube.com hari ini ya."}}
	a.LLM = f2
	if _, _, err := a.DailySummary(context.Background()); err != nil || !strings.Contains(f2.allContent(), "1. youtube.com") {
		t.Fatalf("allowed: %v %q", err, f2.allContent())
	}
}
