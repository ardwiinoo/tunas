// Package llmstats counts LLM attempts per model and per day (PRD Fase 8g),
// so /status can say how many calls went to the primary model, how many to
// the fallbacks and how many failed. Numbers only; no prompts or answers.
package llmstats

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Key is the settings key holding today's counters.
const Key = "llm.models"

// Store is where the counters survive a restart (settings table).
type Store interface {
	GetSetting(ctx context.Context, key string, dst any) (bool, error)
	SetSetting(ctx context.Context, key string, value any) error
}

// Model is one model's counters for a day.
type Model struct {
	OK    int            `json:"ok"`
	Fail  int            `json:"fail"`
	Kinds map[string]int `json:"kinds,omitempty"` // failure kind → count
	MS    int64          `json:"ms"`              // total time of the successful attempts
}

// Day is the persisted shape.
type Day struct {
	Day    string           `json:"day"` // YYYY-MM-DD, local
	Models map[string]Model `json:"models"`
}

// Tracker records attempts. Safe for concurrent use.
type Tracker struct {
	Store Store
	Now   func() time.Time
	Loc   *time.Location

	mu     sync.Mutex
	loaded bool
	cur    Day
}

func (t *Tracker) today() string { return t.Now().In(t.Loc).Format("2006-01-02") }

// load fetches the persisted day once (and starts a fresh one at midnight).
// t.mu must be held.
func (t *Tracker) load(ctx context.Context) {
	if !t.loaded {
		t.loaded = true
		var d Day
		if t.Store != nil {
			_, _ = t.Store.GetSetting(ctx, Key, &d)
		}
		t.cur = d
	}
	if t.cur.Day != t.today() || t.cur.Models == nil {
		t.cur = Day{Day: t.today(), Models: map[string]Model{}}
	}
}

// Record notes one attempt on a model. kind is "" on success.
func (t *Tracker) Record(model string, ok bool, kind string, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load(ctx)
	m := t.cur.Models[model]
	if ok {
		m.OK++
		m.MS += d.Milliseconds()
	} else {
		m.Fail++
		if m.Kinds == nil {
			m.Kinds = map[string]int{}
		}
		m.Kinds[kind]++
	}
	t.cur.Models[model] = m
	if t.Store != nil {
		_ = t.Store.SetSetting(ctx, Key, t.cur)
	}
}

// Today returns a copy of today's counters.
func (t *Tracker) Today(ctx context.Context) Day {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load(ctx)
	out := Day{Day: t.cur.Day, Models: make(map[string]Model, len(t.cur.Models))}
	for k, v := range t.cur.Models {
		out.Models[k] = v
	}
	return out
}

// Names lists the models of a day, the primary first and the rest by name.
func (d Day) Names(primary string) []string {
	names := make([]string, 0, len(d.Models))
	for n := range d.Models {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == primary) != (names[j] == primary) {
			return names[i] == primary
		}
		return names[i] < names[j]
	})
	return names
}

// Totals adds the days's counters: successes on the primary, successes on
// fallbacks, and failed attempts.
func (d Day) Totals(primary string) (primaryOK, fallbackOK, failed int) {
	for n, m := range d.Models {
		if n == primary {
			primaryOK += m.OK
		} else {
			fallbackOK += m.OK
		}
		failed += m.Fail
	}
	return
}
