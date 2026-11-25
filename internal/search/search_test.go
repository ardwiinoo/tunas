package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const key = "tvly-SECRET-key-must-not-leak"

// bigResponse is a realistic worst case: 10 hits with thousands of
// characters each (what the API returns when asked for more than 5).
func bigResponse(n, chars int) string {
	var rs []map[string]any
	for i := 1; i <= n; i++ {
		rs = append(rs, map[string]any{"title": fmt.Sprintf("Judul %d", i), "url": fmt.Sprintf("https://contoh%d.id/a", i),
			"content": strings.Repeat(fmt.Sprintf("isi%d ", i), chars/5), "score": 0.9})
	}
	b, _ := json.Marshal(map[string]any{"query": "x", "answer": "Jawaban singkat Tavily.", "results": rs, "usage": map[string]any{"credits": 1}})
	return string(b)
}

func TestTavilyRequestAndResponse(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(404)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(bigResponse(10, 5000)))
	}))
	defer srv.Close()
	c := NewTavily(key)
	c.BaseURL = srv.URL

	res, err := c.Search(context.Background(), "cuaca jakarta", false)
	if err != nil {
		t.Fatal(err)
	}
	// the request of PRD 6.11
	if auth != "Bearer "+key || got["query"] != "cuaca jakarta" || got["search_depth"] != "basic" ||
		got["max_results"] != float64(5) || got["include_answer"] != "basic" || got["include_usage"] != true ||
		got["include_raw_content"] != false {
		t.Fatalf("request = %v auth=%q", got, auth)
	}
	// a big response is read in full; limiting what goes to the model is assist's job
	if len(res.Results) != 10 || len(res.Results[0].Content) < 4000 || res.Answer != "Jawaban singkat Tavily." || res.Credits != 1 {
		t.Fatalf("response: %d results, content %d, answer %q, credits %d", len(res.Results), len(res.Results[0].Content), res.Answer, res.Credits)
	}

	// the advanced depth only on request, never more than 5 results asked
	c.MaxResults = 50
	if _, err := c.Search(context.Background(), "x", true); err != nil || got["search_depth"] != "advanced" || got["max_results"] != float64(5) {
		t.Fatalf("deep request = %v %v", got, err)
	}
}

func TestTavilyErrors(t *testing.T) {
	cases := map[int]error{401: ErrAuth, 429: ErrRate, 432: ErrQuota, 433: ErrQuota}
	for code, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":{"error":"x"}}`))
		}))
		c := NewTavily(key)
		c.BaseURL = srv.URL
		if _, err := c.Search(context.Background(), "q", false); !errors.Is(err, want) {
			t.Errorf("HTTP %d → %v, want %v", code, err, want)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(502) }))
	defer srv.Close()
	c := NewTavily(key)
	c.BaseURL = srv.URL
	var se *StatusError
	if _, err := c.Search(context.Background(), "q", false); !errors.As(err, &se) || se.Code != 502 {
		t.Fatalf("502 → %v", err)
	}
	if _, err := NewTavily("").Search(context.Background(), "x", false); !errors.Is(err, ErrDisabled) {
		t.Fatal("no key should be disabled")
	}
	if _, err := NewTavily("  ").Search(context.Background(), "x", false); !errors.Is(err, ErrDisabled) {
		t.Fatal("a blank key is no key")
	}
}

func TestTavilyTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := NewTavily(key)
	c.BaseURL = srv.URL
	// the caller's deadline is shorter than the 8 s limit
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Search(ctx, "q", false)
	if !errors.Is(err, ErrTimeout) || time.Since(start) > 2*time.Second {
		t.Fatalf("slow server → %v after %s", err, time.Since(start))
	}
	if BasicTimeout != 8*time.Second {
		t.Fatal("PRD 6.11: 8 s per search")
	}
}

func TestErrorsDoNotLeakTheKey(t *testing.T) {
	c := NewTavily(key)
	c.BaseURL = "http://127.0.0.1:1" // refused
	_, err := c.Search(context.Background(), "q", false)
	if err == nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "/search") {
		t.Fatalf("error text must carry neither the key nor the request URL: %v", err)
	}
}
