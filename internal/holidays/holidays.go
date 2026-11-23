// Package holidays fetches Indonesian public holidays (libur nasional) and
// cuti bersama from the tanggalmerah API and caches them in the database.
package holidays

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// DefaultBase is the API host (source: github.com/upsetdev/tanggalmerah, MIT).
const DefaultBase = "https://tanggalmerah.upset.dev"

// ErrNotAvailable means the API has no data for that year yet (404).
var ErrNotAvailable = errors.New("holidays not available for that year")

// Client calls GET {base}/api/holidays?year=YYYY.
type Client struct {
	Base  string
	HTTP  *http.Client
	Sleep func(context.Context, time.Duration) error // backoff between attempts
}

// NewClient uses a 10 s timeout and real sleeps.
func NewClient(base string) *Client {
	if base == "" {
		base = DefaultBase
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// response is the API shape, verified against the live API on 2026-11-23:
// {"success":true,"data":[{"date":"2026-01-01","day":"Kamis","name":"...","type":"holiday"}],"meta":{...}}
type response struct {
	Success bool `json:"success"`
	Data    []struct {
		Date string `json:"date"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"data"`
}

// Fetch gets one year, with at most 3 attempts (backoff 2 s, 6 s). A 404
// is ErrNotAvailable and is not retried.
func (c *Client) Fetch(ctx context.Context, year int) ([]store.Holiday, error) {
	var lastErr error
	for attempt, wait := range []time.Duration{0, 2 * time.Second, 6 * time.Second} {
		if attempt > 0 {
			if err := c.Sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		hs, err := c.fetchOnce(ctx, year)
		if err == nil || errors.Is(err, ErrNotAvailable) {
			return hs, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func (c *Client) fetchOnce(ctx context.Context, year int) ([]store.Holiday, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/holidays?year=%d", c.Base, year), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "tunas-holidays")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("holiday api returned %d", resp.StatusCode)
	}
	var r response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("holiday api: %w", err)
	}
	if !r.Success {
		return nil, errors.New("holiday api: success=false")
	}
	out := make([]store.Holiday, 0, len(r.Data))
	for _, d := range r.Data {
		day, err := time.Parse(time.DateOnly, d.Date)
		if err != nil || d.Name == "" || (d.Type != "holiday" && d.Type != "leave") || day.Year() != year {
			continue // skip what we do not understand rather than fail the year
		}
		out = append(out, store.Holiday{Day: day, Name: strings.TrimSpace(d.Name), Type: d.Type})
	}
	return out, nil
}

// Store is what the refresher needs from the database.
type Store interface {
	HolidaysFetchedAt(ctx context.Context, year int) (time.Time, error)
	UpsertHolidays(ctx context.Context, hs []store.Holiday, now time.Time) error
}

// Fetcher fetches a year (Client in production).
type Fetcher interface {
	Fetch(ctx context.Context, year int) ([]store.Holiday, error)
}

// Refresher keeps the cache fresh (PRD 6.6).
type Refresher struct {
	API    Fetcher
	DB     Store
	Now    func() time.Time
	Loc    *time.Location
	Log    *slog.Logger
	Notify func(ctx context.Context, text string) error // owner alert (once)

	warned bool
}

// MaxAge is how old a year's data may get before it is fetched again.
const MaxAge = 30 * 24 * time.Hour

// Sync fetches the current year when it is missing or older than MaxAge,
// and from 1 November also next year (404 = not published yet, ignored).
// On failure the cache is used; if this year has no data at all, the
// owner is told once.
func (r *Refresher) Sync(ctx context.Context) {
	now := r.Now().In(r.Loc)
	years := []int{now.Year()}
	if now.Month() >= time.November {
		years = append(years, now.Year()+1)
	}
	for i, y := range years {
		last, err := r.DB.HolidaysFetchedAt(ctx, y)
		if err != nil {
			r.Log.Warn("holiday cache read failed", "err", err)
			return
		}
		if !last.IsZero() && now.Sub(last) < MaxAge {
			continue
		}
		hs, err := r.API.Fetch(ctx, y)
		switch {
		case errors.Is(err, ErrNotAvailable):
			r.Log.Info("holidays not published yet", "year", y)
		case err != nil:
			r.Log.Warn("holiday fetch failed, using cache", "year", y, "err", err)
			if i == 0 && last.IsZero() && !r.warned && r.Notify != nil {
				r.warned = true
				_ = r.Notify(ctx, "⚠️ Data hari libur belum ada (API libur gagal). Pengingat worklog tetap jalan di hari kerja biasa (Senin–Jumat).")
			}
		default:
			if err := r.DB.UpsertHolidays(ctx, hs, now); err != nil {
				r.Log.Warn("holiday cache write failed", "err", err)
				continue
			}
			r.Log.Info("holidays updated", "year", y, "count", len(hs))
		}
	}
}

// Run syncs at start and then daily until ctx is done.
func (r *Refresher) Run(ctx context.Context) {
	for {
		r.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
