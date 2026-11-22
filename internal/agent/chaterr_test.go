package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req["text"] {
		case "slow":
			time.Sleep(300 * time.Millisecond)
		case "cap":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"llm_cap"}`))
		case "llmslow":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"llm_timeout"}`))
		case "llm":
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"llm_failed"}`))
		case "auth":
			w.WriteHeader(401)
		case "proxy":
			w.WriteHeader(502)
		}
		_, _ = w.Write([]byte(`{"reply":"ok"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tnd_secret")

	for text, want := range map[string]ChatFail{
		"cap": FailCap, "llmslow": FailTimeout, "llm": FailLLM, "auth": FailAuth, "proxy": FailOffline,
	} {
		_, err := c.Chat(context.Background(), text, "")
		if got := ClassifyChatError(err); got != want {
			t.Errorf("%s: %v → %s, want %s", text, err, got, want)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Chat(ctx, "slow", ""); ClassifyChatError(err) != FailTimeout {
		t.Errorf("slow: %v", err)
	}

	// nothing listening on this port
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, err := NewClient("http://"+addr, "tnd_secret").Chat(context.Background(), "x", "")
	if ClassifyChatError(err) != FailOffline {
		t.Errorf("refused: %v", err)
	}
	if err == nil || strings.Contains(err.Error(), "tnd_secret") {
		t.Error("error text must not contain the token")
	}
}
