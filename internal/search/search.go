// Package search is a small Tavily web search client (PRD 6.11).
//
// Parameters were checked against docs.tavily.com on 2026-11-25:
// search_depth basic|advanced, max_results, include_answer true|"basic"|
// "advanced", include_raw_content, include_usage (response: usage.credits),
// errors 401 (bad key), 429 (rate), 432/433 (plan or pay-as-you-go limit).
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Result is one search hit. Content can be thousands of characters long;
// never pass it on unbounded (see assist.compactResults).
type Result struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// Response is what a search returns.
type Response struct {
	Answer  string // Tavily's own short answer ("" when none)
	Results []Result
	Credits int           // credits this request used (usage.credits; 0 = not reported)
	Elapsed time.Duration // wall time of the request
}

// Searcher runs a web search. deep = search_depth "advanced" (2 credits).
type Searcher interface {
	Search(ctx context.Context, query string, deep bool) (Response, error)
}

// Errors. Each has its own user-facing message in package assist.
var (
	ErrDisabled = errors.New("web search disabled")           // no key
	ErrAuth     = errors.New("web search key rejected (401)") // wrong / missing / expired
	ErrQuota    = errors.New("web search quota exhausted")    // 432 plan limit, 433 pay-as-you-go limit
	ErrRate     = errors.New("web search rate limited (429)") // too fast
	ErrTimeout  = errors.New("web search timed out")
)

// StatusError is any other non-2xx answer.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("search returned %d", e.Code) }

// Timeouts (PRD 6.11: 8 s per search; the advanced depth is slower).
const (
	BasicTimeout = 8 * time.Second
	DeepTimeout  = 12 * time.Second
	maxBody      = 4 << 20 // a response with 10 long results is far below this
)

// Tavily calls POST {BaseURL}/search.
type Tavily struct {
	BaseURL    string
	APIKey     string
	MaxResults int // default 5
	HTTP       *http.Client
}

// NewTavily creates a client. The key is only ever sent as a bearer token.
func NewTavily(apiKey string) *Tavily {
	return &Tavily{BaseURL: "https://api.tavily.com", APIKey: strings.TrimSpace(apiKey), MaxResults: 5, HTTP: &http.Client{}}
}

// Search implements Searcher.
func (t *Tavily) Search(ctx context.Context, query string, deep bool) (Response, error) {
	if t.APIKey == "" {
		return Response{}, ErrDisabled
	}
	depth, limit := "basic", BasicTimeout
	if deep {
		depth, limit = "advanced", DeepTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	max := t.MaxResults
	if max <= 0 || max > 5 {
		max = 5
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "search_depth": depth, "max_results": max,
		"include_answer": "basic", "include_usage": true, "include_raw_content": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return Response{}, classify(ctx, err)
	}
	defer resp.Body.Close()
	el := time.Since(start)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		drain(resp.Body)
		return Response{Elapsed: el}, ErrAuth
	case http.StatusTooManyRequests:
		drain(resp.Body)
		return Response{Elapsed: el}, ErrRate
	case 432, 433:
		drain(resp.Body)
		return Response{Elapsed: el}, ErrQuota
	}
	if resp.StatusCode/100 != 2 {
		drain(resp.Body)
		return Response{Elapsed: el}, &StatusError{Code: resp.StatusCode}
	}
	var out struct {
		Answer  string   `json:"answer"`
		Results []Result `json:"results"`
		Usage   struct {
			Credits float64 `json:"credits"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil {
		if ctx.Err() != nil {
			return Response{Elapsed: time.Since(start)}, classify(ctx, ctx.Err())
		}
		return Response{Elapsed: time.Since(start)}, fmt.Errorf("search decode: %w", err)
	}
	return Response{Answer: strings.TrimSpace(out.Answer), Results: out.Results, Credits: int(out.Usage.Credits + 0.5), Elapsed: time.Since(start)}, nil
}

func drain(r io.Reader) { _, _ = io.Copy(io.Discard, io.LimitReader(r, 8192)) }

// classify turns transport errors into ErrTimeout where it applies and
// strips the request URL from the rest.
func classify(ctx context.Context, err error) error {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrTimeout
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		err = ue.Unwrap()
	}
	return fmt.Errorf("search request: %w", err)
}
