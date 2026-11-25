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
	"net"
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
	KindCap      = "llm_cap"      // our own daily request cap
	KindDisabled = "llm_disabled" // no key/model configured
	KindBlocked  = "llm_blocked"  // gateway refused the model (401/403/404): configuration problem
	KindQuota    = "llm_quota"    // provider quota or rate limit (429)
	KindTimeout  = "llm_timeout"  // too slow
	KindDown     = "llm_down"     // provider error (5xx, network, empty answer)
	KindFailed   = "llm_failed"   // anything else
)

// Kind classifies a Complete error. For a FailedError (every model failed)
// it is the kind that best explains the whole failure.
func Kind(err error) string {
	var fe *FailedError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &fe):
		return fe.Kind()
	case errors.Is(err, ErrCap):
		return KindCap
	case errors.Is(err, ErrDisabled):
		return KindDisabled
	}
	return attemptKind(err)
}

// attemptKind classifies one model's failure.
func attemptKind(err error) string {
	var se *StatusError
	switch {
	case errors.As(err, &se):
		switch {
		case se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden || se.Code == http.StatusNotFound:
			return KindBlocked
		case se.Code == http.StatusTooManyRequests:
			return KindQuota
		case se.Code >= 500:
			return KindDown
		}
		return KindFailed
	case errors.Is(err, ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	case errors.Is(err, ErrEmpty):
		return KindDown
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return KindDown
	}
	return KindFailed
}

// Attempt is one model's failed try.
type Attempt struct {
	Model string
	Kind  string
	Err   error
}

// FailedError is returned when every model failed.
type FailedError struct{ Attempts []Attempt }

func (e *FailedError) Error() string {
	parts := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		parts[i] = a.Model + ": " + a.Err.Error()
	}
	return fmt.Sprintf("all %d llm models failed (%s)", len(e.Attempts), strings.Join(parts, "; "))
}

// Unwrap exposes every attempt's error to errors.Is/As.
func (e *FailedError) Unwrap() []error {
	errs := make([]error, len(e.Attempts))
	for i, a := range e.Attempts {
		errs[i] = a.Err
	}
	return errs
}

// Kind picks the explanation for the user: blocked only if every model was
// refused (pure configuration problem); otherwise the provider-side cause
// of the models that were allowed to run, preferring "down" over "timeout"
// over "quota" when they differ.
func (e *FailedError) Kind() string {
	seen := map[string]bool{}
	for _, a := range e.Attempts {
		if a.Kind != KindBlocked {
			seen[a.Kind] = true
		}
	}
	switch {
	case len(e.Attempts) == 0:
		return KindTimeout // the budget ran out before any model was tried
	case len(seen) == 0:
		return KindBlocked
	case len(seen) == 1:
		for k := range seen {
			return k
		}
	}
	for _, k := range []string{KindDown, KindTimeout, KindQuota} {
		if seen[k] {
			return k
		}
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
	// OnBlocked is called when the gateway refuses a model (401/403/404),
	// even if a fallback then answers: a configuration problem for the owner.
	OnBlocked func(model string, status int)
	// OnAttempt is called after every model try that was not cancelled by the
	// caller: ok, or the failure kind (see the Kind* constants), and how long
	// it took. Used for the per-model usage in /status.
	OnAttempt func(model string, ok bool, kind string, d time.Duration)
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

// Complete tries each model in order until one answers. Any per-model
// failure (401/403/404, 429, 5xx, timeout, empty answer, network) moves on
// to the next model; only when all have failed does it return a
// *FailedError. Every attempt is logged with the model and its status.
func (c *Client) Complete(ctx context.Context, msgs []Message) (string, error) {
	if c.APIKey == "" || c.Model == "" {
		return "", ErrDisabled
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, c.Total)
	defer cancel()
	fe := &FailedError{}
	models := append([]string{c.Model}, c.Fallbacks...)
	for i, model := range models {
		dl, _ := ctx.Deadline()
		left := time.Until(dl)
		if i > 0 && left < minAttempt { // the primary is always tried
			c.Log.Warn("llm budget exhausted", "skipped_models", len(models)-i, "total_ms", time.Since(start).Milliseconds())
			break
		}
		t0 := time.Now()
		content, err := c.tryModel(ctx, model, msgs, min(c.PerModel, left))
		if err != nil && ctx.Err() != nil && !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			// the caller gave up (owner pressed Esc, shutdown): not an LLM failure
			c.Log.Info("llm request cancelled by the caller", "model", model, "attempt", i+1,
				"ms", time.Since(t0).Milliseconds())
			return "", ctx.Err()
		}
		if err == nil {
			if c.OnAttempt != nil {
				c.OnAttempt(model, true, "", time.Since(t0))
			}
			c.Log.Info("llm attempt", "model", model, "attempt", i+1, "status", "ok",
				"ms", time.Since(t0).Milliseconds(), "total_ms", time.Since(start).Milliseconds())
			return content, nil
		}
		kind := attemptKind(err)
		if c.OnAttempt != nil {
			c.OnAttempt(model, false, kind, time.Since(t0))
		}
		fe.Attempts = append(fe.Attempts, Attempt{Model: model, Kind: kind, Err: err})
		c.Log.Warn("llm attempt", "model", model, "attempt", i+1, "status", statusOf(err), "kind", kind,
			"err", err, "ms", time.Since(t0).Milliseconds())
		var se *StatusError
		if kind == KindBlocked && errors.As(err, &se) && c.OnBlocked != nil {
			c.OnBlocked(model, se.Code)
		}
	}
	c.Log.Warn("llm all models failed", "kind", fe.Kind(), "attempts", len(fe.Attempts),
		"total_ms", time.Since(start).Milliseconds())
	return "", fe
}

// statusOf is the HTTP status for logs, or the error kind when there was none.
func statusOf(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprint(se.Code)
	}
	return attemptKind(err)
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
		if ctx.Err() == nil { // a caller that already gave up is not a gateway failure to warn about
			c.Log.Warn("llm call failed", "model", model, "status", resp.StatusCode, "ms", time.Since(start).Milliseconds())
		}
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
