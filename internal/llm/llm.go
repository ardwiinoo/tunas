// Package llm is a small OpenAI-compatible client for the Bifrost gateway.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Message is a chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Completer produces a completion. Implemented by Client (and fakes in tests).
type Completer interface {
	Complete(ctx context.Context, msgs []Message) (string, error)
}

// Errors.
var (
	ErrDisabled = errors.New("llm disabled")
	ErrCap      = errors.New("llm daily request cap reached")
	ErrEmpty    = errors.New("llm returned empty content")
)

// Client calls POST {base}/chat/completions with Bifrost fallbacks.
type Client struct {
	BaseURL   string
	APIKey    string
	Model     string
	Fallbacks []string
	MaxTokens int
	HTTP      *http.Client
	Log       *slog.Logger
}

// New creates a client with PRD defaults (45s timeout, 800 max tokens).
func New(baseURL, apiKey, model string, fallbacks []string, log *slog.Logger) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Fallbacks: fallbacks,
		MaxTokens: 800, HTTP: &http.Client{Timeout: 45 * time.Second}, Log: log,
	}
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Fallbacks []string  `json:"fallbacks,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	ExtraFields struct {
		RoutingInfo json.RawMessage `json:"routing_info"`
	} `json:"extra_fields"`
}

// StatusError is a non-2xx response from the gateway.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("llm gateway returned %d", e.Code) }

// Complete sends msgs. If the model spent all tokens "thinking" (empty content,
// finish_reason=length), it retries once with double max_tokens.
func (c *Client) Complete(ctx context.Context, msgs []Message) (string, error) {
	if c.APIKey == "" || c.Model == "" {
		return "", ErrDisabled
	}
	content, finish, err := c.call(ctx, msgs, c.MaxTokens)
	if err == nil && content == "" && finish == "length" {
		c.Log.Info("llm empty content with finish_reason=length, retrying with more tokens")
		content, _, err = c.call(ctx, msgs, c.MaxTokens*2)
	}
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", ErrEmpty
	}
	return content, nil
}

func (c *Client) call(ctx context.Context, msgs []Message, maxTokens int) (string, string, error) {
	body, _ := json.Marshal(chatRequest{Model: c.Model, Messages: msgs, MaxTokens: maxTokens, Fallbacks: c.Fallbacks})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("llm request: %w", stripURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		c.Log.Warn("llm call failed", "status", resp.StatusCode, "ms", time.Since(start).Milliseconds())
		return "", "", &StatusError{Code: resp.StatusCode}
	}
	var out chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", "", fmt.Errorf("llm decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", "", ErrEmpty
	}
	ch := out.Choices[0]
	c.Log.Info("llm call", "model", out.Model, "routing_info", string(out.ExtraFields.RoutingInfo),
		"finish_reason", ch.FinishReason, "ms", time.Since(start).Milliseconds())
	return CleanContent(ch.Message.Content), ch.FinishReason, nil
}

// stripURLError avoids echoing request URLs in errors.
func stripURLError(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

var reThink = regexp.MustCompile(`(?s)<think>.*?</think>`)

// CleanContent removes reasoning blocks and surrounding whitespace.
func CleanContent(s string) string {
	return strings.TrimSpace(reThink.ReplaceAllString(s, ""))
}

// ExtractJSON returns the outermost {...} object in s (models often wrap JSON in prose or fences).
func ExtractJSON(s string) (string, bool) {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return "", false
	}
	return s[i : j+1], true
}

// UsageCounter counts requests per day (backed by the llm_usage table).
type UsageCounter interface {
	IncrementLLMUsage(ctx context.Context, day time.Time) (int, error)
}

// Capped enforces a daily request cap before delegating to Inner.
type Capped struct {
	Inner Completer
	Usage UsageCounter
	Cap   int
	Now   func() time.Time
	Loc   *time.Location
}

// Complete implements Completer. Each call (including failures) counts.
func (c *Capped) Complete(ctx context.Context, msgs []Message) (string, error) {
	n, err := c.Usage.IncrementLLMUsage(ctx, c.Now().In(c.Loc))
	if err != nil {
		return "", err
	}
	if n > c.Cap {
		return "", ErrCap
	}
	return c.Inner.Complete(ctx, msgs)
}

// Disabled is a Completer used when no Bifrost key/model is configured.
type Disabled struct{}

// Complete always returns ErrDisabled.
func (Disabled) Complete(context.Context, []Message) (string, error) { return "", ErrDisabled }
