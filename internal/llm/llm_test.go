package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type gateway struct {
	mu       sync.Mutex
	requests []chatRequest
	auth     []string
	respond  func(n int, req chatRequest) (int, string)
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	n := len(g.requests)
	g.mu.Unlock()
	code, body := g.respond(n, req)
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func ok(content, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"model":        "x/served",
		"choices":      []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}, "finish_reason": finish}},
		"extra_fields": map[string]any{"routing_info": map[string]string{"provider": "x"}},
	})
	return string(b)
}

func newClient(t *testing.T, g *gateway) *Client {
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/v1/", "vk-test", "p/primary", []string{"f/one", "f/two"}, slog.New(slog.DiscardHandler))
}

func TestCompleteAuthAndPrimary(t *testing.T) {
	g := &gateway{respond: func(int, chatRequest) (int, string) { return 200, ok("  <think>hmm</think> Halo! ", "stop") }}
	c := newClient(t, g)
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hai"}})
	if err != nil || got != "Halo!" {
		t.Fatalf("got %q %v", got, err)
	}
	r := g.requests[0]
	if len(g.requests) != 1 || r.Model != "p/primary" || r.MaxTokens != 800 || g.auth[0] != "Bearer vk-test" {
		t.Fatalf("request = %+v auth=%q", r, g.auth[0])
	}
}

func TestCompleteFallsBackPerModel(t *testing.T) {
	// primary errors, first fallback hangs past its budget, second answers
	g := &gateway{respond: func(_ int, req chatRequest) (int, string) {
		switch req.Model {
		case "p/primary":
			return 502, `{}`
		case "f/one":
			time.Sleep(400 * time.Millisecond)
			return 200, ok("terlambat", "stop")
		}
		return 200, ok("dari f/two", "stop")
	}}
	c := newClient(t, g)
	c.PerModel, c.Total = 150*time.Millisecond, 3*time.Second
	got, err := c.Complete(context.Background(), nil)
	if err != nil || got != "dari f/two" {
		t.Fatalf("got %q %v", got, err)
	}
	if ms := []string{g.requests[0].Model, g.requests[1].Model, g.requests[2].Model}; ms[0] != "p/primary" || ms[1] != "f/one" || ms[2] != "f/two" {
		t.Fatalf("order = %v", ms)
	}
}

func TestCompleteTotalBudget(t *testing.T) {
	g := &gateway{respond: func(int, chatRequest) (int, string) {
		time.Sleep(time.Second)
		return 200, ok("x", "stop")
	}}
	c := newClient(t, g)
	c.PerModel, c.Total = 300*time.Millisecond, 450*time.Millisecond
	start := time.Now()
	_, err := c.Complete(context.Background(), nil)
	if !errors.Is(err, ErrTimeout) || Kind(err) != KindTimeout {
		t.Fatalf("err = %v", err)
	}
	if el := time.Since(start); el > 900*time.Millisecond {
		t.Fatalf("took %s, total budget not enforced", el)
	}
}

// The reported bug: a 403 on the primary must not stop the fallbacks.
func TestCompleteFallsBackAfter403And429(t *testing.T) {
	g := &gateway{respond: func(_ int, req chatRequest) (int, string) {
		switch req.Model {
		case "p/primary":
			return 403, `{"error":"model not allowed for this key"}`
		case "f/one":
			return 429, `{"error":"quota"}`
		}
		return 200, ok("dari fallback kedua", "stop")
	}}
	c := newClient(t, g)
	var blocked []string
	c.OnBlocked = func(model string, status int) { blocked = append(blocked, fmt.Sprint(model, " ", status)) }
	var logBuf bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&logBuf, nil))

	got, err := c.Complete(context.Background(), nil)
	if err != nil || got != "dari fallback kedua" || len(g.requests) != 3 {
		t.Fatalf("got %q %v after %d requests", got, err, len(g.requests))
	}
	if len(blocked) != 1 || blocked[0] != "p/primary 403" {
		t.Fatalf("owner must hear about the blocked primary even though a fallback answered: %v", blocked)
	}
	log := logBuf.String()
	for _, want := range []string{
		"model=p/primary attempt=1 status=403 kind=llm_blocked",
		"model=f/one attempt=2 status=429 kind=llm_quota",
		"model=f/two attempt=3 status=ok",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestAllFailedKind(t *testing.T) {
	cases := []struct {
		codes []int
		want  string
	}{
		{[]int{403, 403, 403}, KindBlocked}, // only configuration: owner must fix it
		{[]int{401, 404, 403}, KindBlocked},
		{[]int{403, 429, 429}, KindQuota}, // the models allowed to run hit quota
		{[]int{429, 502, 429}, KindDown},
		{[]int{500, 503, 403}, KindDown},
		{[]int{400, 400, 400}, KindFailed},
	}
	for _, tc := range cases {
		g := &gateway{respond: func(n int, _ chatRequest) (int, string) { return tc.codes[n-1], `{}` }}
		c := newClient(t, g)
		_, err := c.Complete(context.Background(), nil)
		var fe *FailedError
		if !errors.As(err, &fe) || len(fe.Attempts) != 3 || len(g.requests) != 3 {
			t.Fatalf("%v: err %v after %d requests", tc.codes, err, len(g.requests))
		}
		if Kind(err) != tc.want {
			t.Errorf("%v: kind %s, want %s", tc.codes, Kind(err), tc.want)
		}
		var se *StatusError
		if !errors.As(err, &se) {
			t.Errorf("%v: attempt errors must stay reachable with errors.As", tc.codes)
		}
	}
}

func TestCallerCancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := &gateway{respond: func(int, chatRequest) (int, string) { cancel(); return 500, `{}` }}
	c := newClient(t, g)
	if _, err := c.Complete(ctx, nil); !errors.Is(err, context.Canceled) || len(g.requests) != 1 {
		t.Fatalf("err %v after %d requests (shutdown must not walk the fallbacks)", err, len(g.requests))
	}
	var logBuf bytes.Buffer
	c.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx2, cancel2 := context.WithCancel(context.Background())
	g.respond = func(int, chatRequest) (int, string) { cancel2(); return 500, `{}` }
	_, _ = c.Complete(ctx2, nil)
	if !strings.Contains(logBuf.String(), "level=INFO msg=\"llm request cancelled by the caller\"") ||
		strings.Contains(logBuf.String(), "level=WARN") {
		t.Fatalf("a cancel by the owner is INFO, not WARN:\n%s", logBuf.String())
	}
}

func TestKind(t *testing.T) {
	for err, want := range map[error]string{
		ErrCap: KindCap, ErrDisabled: KindDisabled, ErrTimeout: KindTimeout,
		context.DeadlineExceeded: KindTimeout, ErrEmpty: KindDown, &StatusError{Code: 500}: KindDown,
		&StatusError{Code: 403}: KindBlocked, &StatusError{Code: 429}: KindQuota, &StatusError{Code: 400}: KindFailed,
	} {
		if got := Kind(err); got != want {
			t.Errorf("Kind(%v) = %s, want %s", err, got, want)
		}
	}
}

func TestCompleteRetriesOnceOnLength(t *testing.T) {
	g := &gateway{respond: func(n int, req chatRequest) (int, string) {
		if n == 1 {
			return 200, ok("", "length")
		}
		return 200, ok("jawab", "stop")
	}}
	c := newClient(t, g)
	got, err := c.Complete(context.Background(), nil)
	if err != nil || got != "jawab" || len(g.requests) != 2 || g.requests[1].MaxTokens != 1600 {
		t.Fatalf("got %q %v, requests %+v", got, err, g.requests)
	}

	g2 := &gateway{respond: func(int, chatRequest) (int, string) { return 200, ok("", "length") }}
	c2 := newClient(t, g2)
	// exactly one retry per model, then the next model
	if _, err := c2.Complete(context.Background(), nil); !errors.Is(err, ErrEmpty) || len(g2.requests) != 6 ||
		g2.requests[1].Model != "p/primary" || g2.requests[2].Model != "f/one" {
		t.Fatalf("err %v after %d requests", err, len(g2.requests))
	}
}

func TestComplete429NoLoop(t *testing.T) {
	// each model is tried once, never the same one twice
	g := &gateway{respond: func(int, chatRequest) (int, string) { return 429, `{"error":"quota"}` }}
	c := newClient(t, g)
	_, err := c.Complete(context.Background(), nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 429 || len(g.requests) != 3 {
		t.Fatalf("err %v, %d requests", err, len(g.requests))
	}
}

func TestDisabled(t *testing.T) {
	c := New("http://x", "", "", nil, slog.New(slog.DiscardHandler))
	if _, err := c.Complete(context.Background(), nil); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
}

type memUsage struct{ n map[string]int }

func (m *memUsage) IncrementLLMUsage(_ context.Context, t time.Time) (int, error) {
	k := t.Format("2006-01-02")
	m.n[k]++
	return m.n[k], nil
}

type echo struct{ calls int }

func (e *echo) Complete(context.Context, []Message) (string, error) { e.calls++; return "ok", nil }

func TestCapped(t *testing.T) {
	now := time.Date(2026, 11, 23, 23, 0, 0, 0, time.UTC)
	inner := &echo{}
	c := &Capped{Inner: inner, Usage: &memUsage{n: map[string]int{}}, Cap: 2, Now: func() time.Time { return now }, Loc: time.UTC}
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Complete(context.Background(), nil); !errors.Is(err, ErrCap) || inner.calls != 2 {
		t.Fatalf("cap not enforced: %v calls=%d", err, inner.calls)
	}
	now = now.Add(2 * time.Hour) // next day
	if _, err := c.Complete(context.Background(), nil); err != nil {
		t.Fatal("cap should reset on a new day")
	}
}

func TestExtractJSON(t *testing.T) {
	got, ok := ExtractJSON("Berikut:\n```json\n{\"a\": {\"b\": 1}}\n```")
	if !ok || got != `{"a": {"b": 1}}` {
		t.Fatalf("got %q", got)
	}
	if _, ok := ExtractJSON("tidak ada"); ok {
		t.Fatal("expected no json")
	}
}

// Every model try is reported to OnAttempt (the per-model usage of /status).
func TestOnAttemptReportsEachTry(t *testing.T) {
	g := &gateway{respond: func(_ int, req chatRequest) (int, string) {
		switch req.Model {
		case "p/primary":
			return 429, `{}`
		case "f/one":
			return 403, `{}`
		}
		return 200, ok("jawaban", "stop")
	}}
	c := newClient(t, g)
	type try struct {
		model, kind string
		ok          bool
	}
	var got []try
	c.OnAttempt = func(model string, ok bool, kind string, _ time.Duration) { got = append(got, try{model, kind, ok}) }
	if _, err := c.Complete(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	want := []try{{"p/primary", KindQuota, false}, {"f/one", KindBlocked, false}, {"f/two", "", true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
}

// A caller that gives up (Esc) is not a model failure.
func TestOnAttemptIgnoresCancellation(t *testing.T) {
	g := &gateway{respond: func(int, chatRequest) (int, string) {
		time.Sleep(300 * time.Millisecond)
		return 200, ok("x", "stop")
	}}
	c := newClient(t, g)
	n := 0
	c.OnAttempt = func(string, bool, string, time.Duration) { n++ }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, _ = c.Complete(ctx, nil)
	if n != 0 {
		t.Fatalf("%d attempts counted for a cancelled request", n)
	}
}
