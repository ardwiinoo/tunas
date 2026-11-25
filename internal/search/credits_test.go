package search

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type memSettings map[string][]byte

func (m memSettings) GetSetting(_ context.Context, key string, dst any) (bool, error) {
	raw, ok := m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, dst)
}

func (m memSettings) SetSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	m[key] = raw
	return err
}

func TestCredits(t *testing.T) {
	ctx := context.Background()
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, jkt)
	db := memSettings{}
	c := &Credits{DB: db, Now: func() time.Time { return now }, Loc: jkt}
	if err := db.SetSetting(ctx, KeyLimit, 10); err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 7; i++ {
		u, lim, warn, err := c.Add(ctx, 1)
		if err != nil || lim != 10 || u.MonthUsed != i || u.DayUsed != i || warn {
			t.Fatalf("add %d: %+v %d %v %v", i, u, lim, warn, err)
		}
	}
	// the 8th credit reaches 80%: one warning, then never again this month
	if _, _, warn, _ := c.Add(ctx, 1); !warn {
		t.Fatal("warning at 80%")
	}
	if _, _, warn, _ := c.Add(ctx, 1); warn {
		t.Fatal("the warning goes out once")
	}
	if done, _, _, _ := c.Exhausted(ctx); done {
		t.Fatal("9/10 is not exhausted")
	}
	c.Add(ctx, 2) // an advanced search: 2 credits → 11
	if done, u, lim, _ := c.Exhausted(ctx); !done || u.MonthUsed != 11 || lim != 10 {
		t.Fatalf("exhausted: %+v %d", u, lim)
	}
	if line, _ := c.Line(ctx); line != "Pencarian web: 11 kredit hari ini, 11/10 bulan ini (sisa 0)" {
		t.Fatalf("line = %q", line)
	}

	// next day: the day counter restarts, the month keeps counting
	now = now.AddDate(0, 0, 1)
	if u, _, _ := c.State(ctx); u.DayUsed != 0 || u.MonthUsed != 11 {
		t.Fatalf("next day: %+v", u)
	}
	// next month: everything restarts (and the warning can come again)
	now = time.Date(2026, 12, 1, 0, 5, 0, 0, jkt)
	if done, u, _, _ := c.Exhausted(ctx); done || u.MonthUsed != 0 || u.Warned {
		t.Fatalf("new month: %+v", u)
	}
	if m, _ := AnswerMode(ctx, db); m != ModeLLM {
		t.Fatalf("default mode %q", m)
	}
	_ = db.SetSetting(ctx, KeyAnswerMode, "tavily")
	if m, _ := AnswerMode(ctx, db); m != ModeTavily {
		t.Fatal("tavily mode")
	}
	_ = db.SetSetting(ctx, KeyAnswerMode, "apa saja")
	if m, _ := AnswerMode(ctx, db); m != ModeLLM {
		t.Fatal("an unknown value means llm")
	}
}
