package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/agentstats"
)

// Event is a server event as seen by the agent.
type Event struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// State is the response of GET /v1/agent/state.
type State struct {
	FormOverride *string   `json:"form_override"`
	Now          time.Time `json:"now"`
}

// Client talks to the Tunas server API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// StatusError is a non-2xx response. Reason is the server's {"error": ...}
// value when it sent one (e.g. "llm_cap" from the chat endpoint).
type StatusError struct {
	Code   int
	Reason string
}

func (e *StatusError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("server returned %d (%s)", e.Code, e.Reason)
	}
	return fmt.Sprintf("server returned %d", e.Code)
}

// NewClient creates a client. The token is sent only as a bearer header.
func NewClient(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 40 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
		if len(e.Error) > 64 {
			e.Error = e.Error[:64]
		}
		return &StatusError{Code: resp.StatusCode, Reason: e.Error}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

// PostActivity uploads buckets.
func (c *Client) PostActivity(ctx context.Context, bs []Bucket) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/activity", map[string]any{"buckets": bs}, nil)
}

// Events long-polls for events with id > after.
func (c *Client) Events(ctx context.Context, after int64, wait time.Duration) ([]Event, error) {
	var out struct {
		Events []Event `json:"events"`
	}
	path := "/v1/agent/events?after=" + strconv.FormatInt(after, 10) + "&wait=" + strconv.Itoa(int(wait.Seconds()))
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Events, err
}

// Ack acknowledges events up to id.
func (c *Client) Ack(ctx context.Context, upTo int64) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/events/ack", map[string]int64{"up_to": upTo}, nil)
}

// State fetches the initial state.
func (c *Client) State(ctx context.Context) (State, error) {
	var s State
	err := c.do(ctx, http.MethodGet, "/v1/agent/state", nil, &s)
	return s, err
}

// Heartbeat reports the agent's version and tracking state; the server
// answers with the number of events still waiting for this device.
func (c *Client) Heartbeat(ctx context.Context, hb Heartbeat) (int, error) {
	var out struct {
		Pending int `json:"pending_events"`
	}
	body := map[string]any{"version": hb.Version, "paused": hb.Paused}
	if hb.Stats != nil {
		body["stats"] = hb.Stats
	}
	err := c.do(ctx, http.MethodPost, "/v1/agent/heartbeat", body, &out)
	return out.Pending, err
}

// Heartbeat is what the agent reports every minute.
type Heartbeat struct {
	Version string
	Paused  bool
	Stats   *agentstats.Stats // the latest self-sample (nil before the first)
}

// ChatReply is the server's answer to a bubble message. A non-empty
// ConfirmID means Reply is a proposal (reminder/task) that is saved only
// after Confirm(id, true).
type ChatReply struct {
	Reply     string       `json:"reply"`
	ConfirmID string       `json:"confirm_id"`
	Search    bool         `json:"search"`  // a web-search answer
	Sources   []ChatSource `json:"sources"` // shown after the text
}

// ChatSource is a web result an answer points to.
type ChatSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// ChatTurn is one earlier message of the bubble conversation.
type ChatTurn struct {
	Role string `json:"role"` // "user" | "assistant"
	Text string `json:"text"`
}

// Chat sends overlay text (with the conversation so far) and returns
// Tunas's reply.
func (c *Client) Chat(ctx context.Context, text, form string, history []ChatTurn) (ChatReply, error) {
	var out ChatReply
	body := map[string]any{"text": text, "form": form}
	if len(history) > 0 {
		body["history"] = history
	}
	err := c.do(ctx, http.MethodPost, "/v1/agent/chat", body, &out)
	return out, err
}

// Confirm answers a proposal (yes = save it) and returns what happened.
func (c *Client) Confirm(ctx context.Context, id string, yes bool) (string, error) {
	var out struct {
		Reply string `json:"reply"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/agent/chat/confirm", map[string]any{"id": id, "yes": yes}, &out)
	return out.Reply, err
}
