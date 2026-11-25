package bot

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestTodayShowsProjectsAndSettingDefaultsOff(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, loc)
	c := &Commands{DB: db, Loc: loc, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}

	d, _, _ := db.CreateDevice(ctx, "laptop")
	start := time.Date(2026, 11, 25, 9, 0, 0, 0, loc)
	var bs []store.Bucket
	var pb []store.ProjectBucket
	for i := 0; i < 60; i++ {
		bs = append(bs, store.Bucket{Start: start.Add(time.Duration(i) * time.Minute), App: "Code.exe", Category: "work", ActiveSeconds: 60})
		pb = append(pb, store.ProjectBucket{Start: start.Add(time.Duration(i) * time.Minute), Project: "project-A", Editor: "code.exe", ActiveSeconds: 60})
	}
	if err := db.UpsertBuckets(ctx, d.ID, bs); err != nil {
		t.Fatal(err)
	}
	got := c.Handle(ctx, "/today")
	if strings.Contains(got, "Proyek:") {
		t.Fatalf("no project data yet:\n%s", got)
	}
	if _, err := db.UpsertProjectBuckets(ctx, d.ID, pb); err != nil {
		t.Fatal(err)
	}
	if got := c.Handle(ctx, "/today"); !strings.Contains(got, "Proyek:\n• project-A 1j (VS Code)") {
		t.Fatalf("/today:\n%s", got)
	}

	if got := c.Handle(ctx, "/settings"); !strings.Contains(got, "proyek_llm: off") {
		t.Fatalf("project names go to the LLM only when allowed (default off):\n%s", got)
	}
	if got := c.Handle(ctx, "/settings proyek_llm on"); !strings.Contains(got, "proyek_llm sekarang on") {
		t.Fatalf("switch: %q", got)
	}
	if got := c.Handle(ctx, "/settings"); !strings.Contains(got, "proyek_llm: on") {
		t.Fatalf("after switching on:\n%s", got)
	}
}
