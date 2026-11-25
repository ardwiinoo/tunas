package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestStatusSummaryEndpoint(t *testing.T) {
	db := storetest.New(t)
	_, tok, _ := db.CreateDevice(context.Background(), "laptop")
	fail := false
	srv := httptest.NewServer(New(slog.New(slog.DiscardHandler), Options{DB: db,
		StatusSummary: func(context.Context) (string, error) {
			if fail {
				return "", errors.New("pq: password authentication failed for user tunas")
			}
			return "Server Tunas: 38 MB √", nil
		}}))
	defer srv.Close()
	h := &harness{t: t, srv: srv, token: tok}

	if code, _ := h.do("GET", "/v1/agent/status/summary", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("without a token = %d", code)
	}
	code, out := h.do("GET", "/v1/agent/status/summary", tok, nil)
	if code != 200 || out["text"] != "Server Tunas: 38 MB √" {
		t.Fatalf("summary = %d %v", code, out)
	}
	fail = true
	code, out = h.do("GET", "/v1/agent/status/summary", tok, nil)
	if code != http.StatusInternalServerError || len(out) == 0 {
		t.Fatalf("failure = %d %v", code, out)
	}
	for _, v := range out {
		if s, _ := v.(string); len(s) > 0 && (s == "pq: password authentication failed for user tunas") {
			t.Fatal("the internal error text leaked to the client")
		}
	}
}
