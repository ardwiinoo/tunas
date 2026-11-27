package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

// secRig is an API server with a fake clock, a recorded log and a channel for
// the warnings the owner would get on Telegram.
type secRig struct {
	t     *testing.T
	db    *store.DB
	srv   *httptest.Server
	token string
	base  time.Time
	mu    sync.Mutex
	now   time.Time
	notes chan string
	logs  *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newSecRig(t *testing.T, days int) *secRig {
	t.Helper()
	db := storetest.New(t)
	_, token, err := db.CreateDeviceDays(context.Background(), "laptop", days)
	if err != nil {
		t.Fatal(err)
	}
	r := &secRig{t: t, db: db, token: token, base: time.Now(), notes: make(chan string, 20), logs: &syncBuf{}}
	r.now = r.base
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.srv = httptest.NewServer(New(log, Options{DB: db, RatePerMinute: 100000, RateBurst: 100000,
		Now: func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now },
		Notify: func(_ context.Context, text string) error {
			r.notes <- text
			return nil
		}}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *secRig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

// get calls an authenticated endpoint from the given client address.
func (r *secRig) get(token, ip string) int {
	r.t.Helper()
	req, _ := http.NewRequest("GET", r.srv.URL+"/v1/agent/state", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (r *secRig) note() string {
	r.t.Helper()
	select {
	case s := <-r.notes:
		return s
	case <-time.After(3 * time.Second):
		r.t.Fatalf("expected a warning to the owner, got none; log: %s", r.logs.String())
		return ""
	}
}

func (r *secRig) noNote() {
	r.t.Helper()
	select {
	case s := <-r.notes:
		r.t.Fatalf("unexpected warning: %s", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// A token stops working after its lifetime; 14 days before, the owner is
// warned once.
func TestTokenExpiresAndWarnsOnce(t *testing.T) {
	r := newSecRig(t, 30)
	if code := r.get(r.token, "1.1.1.1"); code != 200 {
		t.Fatalf("fresh token → %d", code)
	}
	r.advance(10 * 24 * time.Hour) // 20 days left
	if code := r.get(r.token, "1.1.1.1"); code != 200 {
		t.Fatalf("→ %d", code)
	}
	r.noNote()
	r.advance(7 * 24 * time.Hour) // 13 days left
	if code := r.get(r.token, "1.1.1.1"); code != 200 {
		t.Fatalf("→ %d", code)
	}
	if n := r.note(); !strings.Contains(n, `"laptop"`) || !strings.Contains(n, "13 hari") || !strings.Contains(n, "--set-token") {
		t.Fatalf("warning %q", n)
	}
	for i := 0; i < 3; i++ { // once only
		r.get(r.token, "1.1.1.1")
	}
	r.noNote()
	r.advance(14 * 24 * time.Hour) // past expiry
	if code := r.get(r.token, "1.1.1.1"); code != 401 {
		t.Fatalf("an expired token → %d", code)
	}
}

// A new client address warns once per 24 hours and never blocks.
func TestNewAddressWarnsWithCooldown(t *testing.T) {
	r := newSecRig(t, 180)
	if code := r.get(r.token, "203.0.113.7"); code != 200 {
		t.Fatal(code)
	}
	r.get(r.token, "203.0.113.7")
	r.noNote() // first address ever, and the same one again: nothing to say

	if code := r.get(r.token, "198.51.100.23"); code != 200 { // not blocked
		t.Fatalf("a new address is not refused: %d", code)
	}
	n := r.note()
	if !strings.Contains(n, "198.51.*.*") || !strings.Contains(n, "203.0.*.*") {
		t.Fatalf("warning %q", n)
	}
	if strings.Contains(n, "198.51.100.23") || strings.Contains(n, "203.0.113.7") || strings.Contains(n, r.token) {
		t.Fatalf("the warning shows a full address or the token: %q", n)
	}
	r.get(r.token, "192.0.2.9") // another change inside the cooldown
	r.noNote()
	r.advance(25 * time.Hour)
	r.get(r.token, "192.0.2.77")
	if n := r.note(); !strings.Contains(n, "192.0.*.*") {
		t.Fatalf("warning after the cooldown %q", n)
	}
}

// 10 failed authentications in 10 minutes warn once (24 h cooldown); neither
// the log nor the warning holds a token.
func TestRepeatedFailuresWarn(t *testing.T) {
	r := newSecRig(t, 180)
	const guess = "tnd_GUESSGUESSGUESSGUESSGUESS0123456789"
	for i := 0; i < 9; i++ {
		if code := r.get(guess, "198.51.100.200"); code != 401 {
			t.Fatal(code)
		}
		r.advance(10 * time.Second)
	}
	r.noNote()
	r.get(guess, "198.51.100.200") // the 10th
	n := r.note()
	if !strings.Contains(n, "10 percobaan") || !strings.Contains(n, "198.51.*.*") {
		t.Fatalf("warning %q", n)
	}
	for i := 0; i < 15; i++ { // more failures: no second warning
		r.get(guess, "198.51.100.200")
	}
	r.noNote()
	// a failure inside the window does not count once it is older than the window
	r.advance(11 * time.Minute)
	r.get(guess, "198.51.100.200")
	r.noNote()

	logs := r.logs.String()
	if strings.Contains(logs, guess) || strings.Contains(logs, "GUESSGUESS") || strings.Contains(logs, r.token) {
		t.Fatal("a token is in the log")
	}
	if !strings.Contains(logs, "198.51.100.200") || !strings.Contains(logs, "failures_in_window") {
		t.Fatalf("the log lacks the address and the count:\n%s", logs)
	}
	if strings.Contains(n, guess) {
		t.Fatal("the warning holds the guessed token")
	}

	// after the cooldown a new burst warns again
	r.advance(25 * time.Hour)
	for i := 0; i < 10; i++ {
		r.get("", "198.51.100.201") // no token at all also counts
	}
	if n := r.note(); !strings.Contains(n, "10 percobaan") {
		t.Fatalf("second warning %q", n)
	}
}

// The token is matched through its hash: the raw token is not stored.
func TestTokenIsStoredHashed(t *testing.T) {
	r := newSecRig(t, 180)
	var raw int
	if err := r.db.Pool.QueryRow(context.Background(),
		`select count(*) from devices where token_hash = $1 or name = $1`, r.token).Scan(&raw); err != nil || raw != 0 {
		t.Fatalf("the raw token is stored (%d, %v)", raw, err)
	}
	var hashed int
	if err := r.db.Pool.QueryRow(context.Background(),
		`select count(*) from devices where token_hash = $1`, store.HashToken(r.token)).Scan(&hashed); err != nil || hashed != 1 {
		t.Fatalf("the hash is not stored (%d, %v)", hashed, err)
	}
	if len(store.HashToken(r.token)) != 64 {
		t.Fatal("not a sha256 hex")
	}
}
