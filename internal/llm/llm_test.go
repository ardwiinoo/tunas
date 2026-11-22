package llm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestCompleteAuthErrorStops(t *testing.T) {
	g := &gateway{respond: func(int, chatRequest) (int, string) { return 401, `{}` }}
	c := newClient(t, g)
	if _, err := c.Complete(context.Background(), nil); Kind(err) != KindFailed || len(g.requests) != 1 {
		t.Fatalf("err %v after %d requests", err, len(g.requests))
	}
}

func TestKind(t *testing.T) {
	for err, want := range map[error]string{
		ErrCap: KindCap, ErrDisabled: KindDisabled, ErrTimeout: KindTimeout,
		context.DeadlineExceeded: KindTimeout, ErrEmpty: KindFailed, &StatusError{Code: 500}: KindFailed,
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
