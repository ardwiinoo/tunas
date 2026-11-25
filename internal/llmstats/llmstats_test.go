package llmstats

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type memStore struct{ raw map[string][]byte }

func (m *memStore) GetSetting(_ context.Context, k string, dst any) (bool, error) {
	b, ok := m.raw[k]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, dst)
}

func (m *memStore) SetSetting(_ context.Context, k string, v any) error {
	b, _ := json.Marshal(v)
	m.raw[k] = b
	return nil
}

func TestCountsPerModelAndPersists(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	st := &memStore{raw: map[string][]byte{}}
	tr := &Tracker{Store: st, Now: func() time.Time { return now }, Loc: time.UTC}
	tr.Record("groq/a", true, "", 800*time.Millisecond)
	tr.Record("groq/a", true, "", 1200*time.Millisecond)
	tr.Record("groq/a", false, "llm_quota", time.Second)
	tr.Record("or/b", true, "", time.Second)
	p, f, fail := tr.Today(ctx).Totals("groq/a")
	if p != 2 || f != 1 || fail != 1 {
		t.Fatalf("totals %d %d %d", p, f, fail)
	}
	if m := tr.Today(ctx).Models["groq/a"]; m.MS != 2000 || m.Kinds["llm_quota"] != 1 {
		t.Fatalf("model %+v", m)
	}
	if names := tr.Today(ctx).Names("groq/a"); names[0] != "groq/a" || names[1] != "or/b" {
		t.Fatalf("names %v", names)
	}

	// a restart keeps today's numbers
	tr2 := &Tracker{Store: st, Now: func() time.Time { return now }, Loc: time.UTC}
	if p, _, _ := tr2.Today(ctx).Totals("groq/a"); p != 2 {
		t.Fatalf("after restart %d", p)
	}
	// the next local day starts from zero
	now = now.Add(24 * time.Hour)
	if p, f, fail := tr2.Today(ctx).Totals("groq/a"); p+f+fail != 0 {
		t.Fatalf("new day %d %d %d", p, f, fail)
	}
}
