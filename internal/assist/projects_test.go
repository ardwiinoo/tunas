package assist

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/projreport"
	"github.com/ardwiinoo/tunas/internal/store"
)

func seedProjects(t *testing.T, db *store.DB, day time.Time) {
	t.Helper()
	ctx := context.Background()
	d, _, err := db.CreateDevice(ctx, "laptop2")
	if err != nil {
		t.Fatal(err)
	}
	var pb []store.ProjectBucket
	for i := 0; i < 90; i++ {
		pb = append(pb, store.ProjectBucket{Start: day.Add(time.Duration(i) * time.Minute), Project: "project-A", Editor: "code.exe", ActiveSeconds: 60})
	}
	if _, err := db.UpsertProjectBuckets(ctx, d.ID, pb); err != nil {
		t.Fatal(err)
	}
}

// Project names stay off the LLM unless the owner allows it: /tanya about
// projects is answered from the facts directly.
func TestTopProjectsStayOffTheLLMByDefault(t *testing.T) {
	f := &fakeLLM{replies: []string{`{"template":"top_projects","range":"this_week"}`, "Kamu paling lama di project-A."}}
	a, db, _ := setup(t, f)
	day := time.Date(2026, 11, 23, 8, 0, 0, 0, jkt)
	seedActivity(t, db, day)
	seedProjects(t, db, day)

	got := a.Ask(context.Background(), "minggu ini aku paling lama di proyek apa?")
	if !strings.Contains(got, "1. project-A 1j30m (VS Code)") {
		t.Fatalf("answer = %q", got)
	}
	if len(f.seen) != 1 {
		t.Fatalf("%d LLM calls, want 1 (only the template choice)", len(f.seen))
	}
	if all := f.allContent(); strings.Contains(all, "project-A") {
		t.Fatalf("a project name reached the LLM: %q", all)
	}

	// allowed: the model phrases the answer from the facts
	if err := db.SetSetting(context.Background(), projreport.KeyIncludeLLM, true); err != nil {
		t.Fatal(err)
	}
	f.replies = []string{`{"template":"top_projects","range":"this_week"}`, "Paling lama di project-A, 1 j 30 m."}
	got = a.Ask(context.Background(), "minggu ini aku paling lama di proyek apa?")
	if !strings.Contains(got, "Paling lama di project-A") || !strings.Contains(f.allContent(), "1. project-A 1j30m") {
		t.Fatalf("allowed: %q", got)
	}
}
