package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTavily(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["query"] == "habis" {
			w.WriteHeader(432)
			return
		}
		_, _ = w.Write([]byte(`{"query":"x","answer":null,"images":[],"response_time":0.5,
			"results":[{"title":"Judul","url":"https://contoh.id/a","content":"isi","score":0.9}]}`))
	}))
	defer srv.Close()
	c := NewTavily("tvly-test")
	c.BaseURL = srv.URL

	rs, err := c.Search(context.Background(), "cuaca jakarta")
	if err != nil || len(rs) != 1 || rs[0].URL != "https://contoh.id/a" {
		t.Fatalf("results = %+v %v", rs, err)
	}
	if auth != "Bearer tvly-test" || got["search_depth"] != "basic" || got["max_results"].(float64) != 5 {
		t.Fatalf("request = %v auth=%q", got, auth)
	}
	if _, err := c.Search(context.Background(), "habis"); !errors.Is(err, ErrQuota) {
		t.Fatalf("432 → %v", err)
	}
	if _, err := NewTavily("").Search(context.Background(), "x"); !errors.Is(err, ErrDisabled) {
		t.Fatal("no key should be disabled")
	}
}
