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
	ErrTimeout  = errors.New("llm timed out")
)

// Failure kinds, for user-facing messages and the agent chat API.
const (
	KindCap      = "llm_cap"
	KindTimeout  = "llm_timeout"
	KindDisabled = "llm_disabled"
	KindFailed   = "llm_failed"
)

// Kind classifies a Complete error.
func Kind(err error) string {
	switch {
	case errors.Is(err, ErrCap):
		return KindCap
	case errors.Is(err, ErrDisabled):
		return KindDisabled
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	return KindFailed
}

// Client calls POST {base}/chat/completions. It walks the model list itself
// (primary, then fallbacks) so each model gets its own time budget: a slow
// model is abandoned after PerModel and the whole call never exceeds Total.
type Client struct {
	BaseURL   string
	APIKey    string
	Model     string
	Fallbacks []string
	MaxTokens int
	PerModel  time.Duration // budget for one model (including the length retry)
	Total     time.Duration // budget for the whole call
	HTTP      *http.Client
	Log       *slog.Logger
}

// Default budgets.
const (
	DefaultPerModel = 9 * time.Second
	DefaultTotal    = 20 * time.Second
	minAttempt      = 2 * time.Second // do not start a model with less time than this
)

// New creates a client with defaults (800 max tokens, 9s per model, 20s total).
func New(baseURL, apiKey, model string, fallbacks []string, log *slog.Logger) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Fallbacks: fallbacks,
		MaxTokens: 800, PerModel: DefaultPerModel, Total: DefaultTotal,
		HTTP: &http.Client{Timeout: DefaultTotal + 5*time.Second}, Log: log,
	}
}

type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
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

// Complete tries each model in order until one answers. Timeouts, network
// errors, 429/5xx and empty answers move on to the next model; an auth
// error (401/403) stops at once since every model would fail the same way.
func (c *Client) Complete(ctx context.Context, msgs []Message) (string, error) {
	if c.APIKey == "" || c.Model == "" {
		return "", ErrDisabled
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, c.Total)
	defer cancel()
	var lastErr error
	models := append([]string{c.Model}, c.Fallbacks...)
	for i, model := range models {
		dl, _ := ctx.Deadline()
		left := time.Until(dl)
		if left < minAttempt {
			if lastErr == nil {
				lastErr = ErrTimeout
			}
			break
		}
		content, err := c.tryModel(ctx, model, msgs, min(c.PerModel, left))
		if err == nil {
			c.Log.Info("llm served", "model", model, "attempt", i+1, "total_ms", time.Since(start).Milliseconds())
			return content, nil
		}
		lastErr = err
		c.Log.Warn("llm model failed", "model", model, "attempt", i+1, "kind", Kind(err), "err", err,
			"total_ms", time.Since(start).Milliseconds())
		if ctx.Err() != nil && !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			break // caller cancelled
		}
		var se *StatusError
		if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
			break
		}
	}
	c.Log.Warn("llm all models failed", "kind", Kind(lastErr), "total_ms", time.Since(start).Milliseconds())
	return "", lastErr
}

// tryModel calls one model within budget. If the model spent all tokens
// "thinking" (empty content, finish_reason=length), it retries once with
// double max_tokens.
func (c *Client) tryModel(ctx context.Context, model string, msgs []Message, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	content, finish, err := c.call(ctx, model, msgs, c.MaxTokens)
	if err == nil && content == "" && finish == "length" {
		c.Log.Info("llm empty content with finish_reason=length, retrying with more tokens", "model", model)
		content, _, err = c.call(ctx, model, msgs, c.MaxTokens*2)
	}
	if err != nil {
		if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w after %s: %w", ErrTimeout, budget.Round(time.Millisecond), err)
		}
		return "", err
	}
	if content == "" {
		return "", ErrEmpty
	}
	return content, nil
}

func (c *Client) call(ctx context.Context, model string, msgs []Message, maxTokens int) (string, string, error) {
	body, _ := json.Marshal(chatRequest{Model: model, Messages: msgs, MaxTokens: maxTokens})
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
		c.Log.Warn("llm call failed", "model", model, "status", resp.StatusCode, "ms", time.Since(start).Milliseconds())
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
