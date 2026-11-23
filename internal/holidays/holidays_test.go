package holidays

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// sample is shaped like the live API response (checked 2026-11-23), with
// made-up names; one row is invalid on purpose.
const sample = `{"success":true,"data":[
 {"date":"2026-01-01","day":"Kamis","name":"Libur contoh satu","type":"holiday"},
 {"date":"2026-02-16","day":"Senin","name":"Cuti contoh","type":"leave"},
 {"date":"2026-02-30","day":"?","name":"Tanggal rusak","type":"holiday"},
 {"date":"2026-03-01","day":"Minggu","name":"Jenis aneh","type":"party"}
],"meta":{"total":4,"year":2026}}`

func noSleep(context.Context, time.Duration) error { return nil }

func TestClientFetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch r.URL.Query().Get("year") {
		case "2026":
			_, _ = w.Write([]byte(sample))
		case "2027":
			w.WriteHeader(404)
		case "2028": // flaky: fails twice, then works
			if n%3 != 0 {
				w.WriteHeader(502)
				return
			}
			_, _ = w.Write([]byte(strings.ReplaceAll(sample, "2026-", "2028-")))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	c.Sleep = noSleep

	hs, err := c.Fetch(context.Background(), 2026)
	if err != nil || len(hs) != 2 || hs[0].Type != "holiday" || hs[1].Type != "leave" || hs[1].Day.Format(time.DateOnly) != "2026-02-16" {
		t.Fatalf("2026 = %+v %v", hs, err)
	}
	calls.Store(0)
	if _, err := c.Fetch(context.Background(), 2027); err != ErrNotAvailable || calls.Load() != 1 {
		t.Fatalf("404 must not be retried: %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	if hs, err := c.Fetch(context.Background(), 2028); err != nil || len(hs) != 2 || calls.Load() != 3 {
		t.Fatalf("retries: %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.Fetch(context.Background(), 2030); err == nil || calls.Load() != 3 {
		t.Fatalf("at most 3 attempts: %v after %d", err, calls.Load())
	}
}

type fakeAPI struct {
	years []int
	err   error
}

func (f *fakeAPI) Fetch(_ context.Context, year int) ([]store.Holiday, error) {
	f.years = append(f.years, year)
	if f.err != nil {
		return nil, f.err
	}
	if year == 2027 {
		return nil, ErrNotAvailable
	}
	return []store.Holiday{{Day: time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC), Name: "x", Type: "holiday"}}, nil
}

type fakeStore struct {
	fetched map[int]time.Time
	upserts int
}

func (f *fakeStore) HolidaysFetchedAt(_ context.Context, year int) (time.Time, error) {
	return f.fetched[year], nil
}

func (f *fakeStore) UpsertHolidays(_ context.Context, hs []store.Holiday, now time.Time) error {
	f.upserts++
	for _, h := range hs {
		f.fetched[h.Day.Year()] = now
	}
	return nil
}

func TestRefresher(t *testing.T) {
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 10, 5, 9, 0, 0, 0, jkt)
	api := &fakeAPI{}
	db := &fakeStore{fetched: map[int]time.Time{}}
	var notes []string
	r := &Refresher{API: api, DB: db, Now: func() time.Time { return now }, Loc: jkt, Log: slog.New(slog.DiscardHandler),
		Notify: func(_ context.Context, s string) error { notes = append(notes, s); return nil }}
	ctx := context.Background()

	r.Sync(ctx)
	if len(api.years) != 1 || api.years[0] != 2026 || db.upserts != 1 {
		t.Fatalf("first sync fetched %v", api.years)
	}
	now = now.AddDate(0, 0, 10)
	r.Sync(ctx)
	if len(api.years) != 1 {
		t.Fatal("fresh cache: no request")
	}
	now = now.AddDate(0, 0, 25) // 9 Nov: data 35 days old, and next year is due
	r.Sync(ctx)
	if len(api.years) != 3 || api.years[1] != 2026 || api.years[2] != 2027 {
		t.Fatalf("from November next year too: %v", api.years)
	}
	if len(notes) != 0 {
		t.Fatal("404 for next year is not an error")
	}

	// failing API with an empty cache: one warning; with a cache: none
	api2 := &fakeAPI{err: context.DeadlineExceeded}
	r2 := &Refresher{API: api2, DB: &fakeStore{fetched: map[int]time.Time{}}, Now: func() time.Time { return now }, Loc: jkt,
		Log: slog.New(slog.DiscardHandler), Notify: func(_ context.Context, s string) error { notes = append(notes, s); return nil }}
	r2.Sync(ctx)
	r2.Sync(ctx)
	if len(notes) != 1 || !strings.Contains(notes[0], "tetap jalan di hari kerja") {
		t.Fatalf("warnings = %q", notes)
	}
	r3 := &Refresher{API: api2, DB: &fakeStore{fetched: map[int]time.Time{2026: now.AddDate(0, -2, 0)}}, Now: func() time.Time { return now },
		Loc: jkt, Log: slog.New(slog.DiscardHandler), Notify: func(_ context.Context, s string) error { notes = append(notes, s); return nil }}
	r3.Sync(ctx)
	if len(notes) != 1 {
		t.Fatal("a stale cache is used silently")
	}
}
