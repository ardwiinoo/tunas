package search

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Settings keys.
const (
	KeyCredits    = "search.credits"       // Usage, JSON
	KeyAnswerMode = "search.answer_mode"   // "llm" (default) | "tavily"
	KeyLimit      = "search.monthly_limit" // credits per month (default DefaultLimit)
)

// Answer modes (PRD 6.11).
const (
	ModeLLM    = "llm"
	ModeTavily = "tavily"
)

// DefaultLimit is Tavily's free plan: 1.000 credits a month.
const DefaultLimit = 1000

// WarnAt is the share of the monthly limit that triggers one warning.
const WarnAt = 0.8

// SettingsStore is the part of store.DB the counter needs.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string, dst any) (bool, error)
	SetSetting(ctx context.Context, key string, value any) error
}

// Usage is the stored counter. Day and Month roll over by themselves.
type Usage struct {
	Day       string `json:"day"` // YYYY-MM-DD
	DayUsed   int    `json:"day_used"`
	Month     string `json:"month"` // YYYY-MM
	MonthUsed int    `json:"month_used"`
	Warned    bool   `json:"warned"` // the 80% warning went out this month
}

// Credits counts the credits Tavily reports (usage.credits) per day and
// month, warns at 80% and tells when the month's allowance is used up.
type Credits struct {
	DB  SettingsStore
	Now func() time.Time
	Loc *time.Location

	mu sync.Mutex
}

func (c *Credits) keys() (day, month string) {
	n := c.Now().In(c.Loc)
	return n.Format(time.DateOnly), n.Format("2006-01")
}

func (c *Credits) load(ctx context.Context) (Usage, error) {
	var u Usage
	if _, err := c.DB.GetSetting(ctx, KeyCredits, &u); err != nil {
		return u, err
	}
	day, month := c.keys()
	if u.Month != month {
		u = Usage{Month: month}
	}
	if u.Day != day {
		u.Day, u.DayUsed = day, 0
	}
	return u, nil
}

// Limit is the monthly allowance.
func (c *Credits) Limit(ctx context.Context) (int, error) {
	n := DefaultLimit
	if _, err := c.DB.GetSetting(ctx, KeyLimit, &n); err != nil {
		return 0, err
	}
	if n <= 0 {
		n = DefaultLimit
	}
	return n, nil
}

// State returns the counter and the limit.
func (c *Credits) State(ctx context.Context) (Usage, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, err := c.load(ctx)
	if err != nil {
		return u, 0, err
	}
	lim, err := c.Limit(ctx)
	return u, lim, err
}

// Exhausted reports whether this month's allowance is used up (no more
// API calls then).
func (c *Credits) Exhausted(ctx context.Context) (bool, Usage, int, error) {
	u, lim, err := c.State(ctx)
	return err == nil && u.MonthUsed >= lim, u, lim, err
}

// Add counts n credits. warn is true exactly once per month, when usage
// first reaches 80% of the limit.
func (c *Credits) Add(ctx context.Context, n int) (u Usage, limit int, warn bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, err = c.load(ctx); err != nil {
		return u, 0, false, err
	}
	if limit, err = c.Limit(ctx); err != nil {
		return u, 0, false, err
	}
	u.DayUsed += n
	u.MonthUsed += n
	if !u.Warned && float64(u.MonthUsed) >= WarnAt*float64(limit) {
		u.Warned, warn = true, true
	}
	return u, limit, warn, c.DB.SetSetting(ctx, KeyCredits, u)
}

// Line is the /status line: "Pencarian web: 3 kredit hari ini, 87/1000 bulan ini (sisa 913)".
func (c *Credits) Line(ctx context.Context) (string, error) {
	u, lim, err := c.State(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Pencarian web: %d kredit hari ini, %d/%d bulan ini (sisa %d)", u.DayUsed, u.MonthUsed, lim, max(lim-u.MonthUsed, 0)), nil
}

// AnswerMode reads the answer mode (llm unless set to tavily).
func AnswerMode(ctx context.Context, db SettingsStore) (string, error) {
	m := ModeLLM
	if _, err := db.GetSetting(ctx, KeyAnswerMode, &m); err != nil {
		return ModeLLM, err
	}
	if m != ModeTavily {
		m = ModeLLM
	}
	return m, nil
}
