// Package search is a small Tavily web search client.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Searcher runs a web search.
type Searcher interface {
	Search(ctx context.Context, query string) ([]Result, error)
}

// Errors.
var (
	ErrDisabled = errors.New("web search disabled")
	ErrQuota    = errors.New("web search quota exhausted")
)

// Tavily calls POST {BaseURL}/search (basic depth = 1 credit per query).
type Tavily struct {
	BaseURL    string
	APIKey     string
	MaxResults int
	HTTP       *http.Client
}

// NewTavily creates a client with sensible defaults.
func NewTavily(apiKey string) *Tavily {
	return &Tavily{BaseURL: "https://api.tavily.com", APIKey: apiKey, MaxResults: 5, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Search implements Searcher.
func (t *Tavily) Search(ctx context.Context, query string) ([]Result, error) {
	if t.APIKey == "" {
		return nil, ErrDisabled
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "search_depth": "basic", "max_results": t.MaxResults,
		"include_answer": false, "topic": "general",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			err = ue.Unwrap() // drop the URL from the message
		}
		return nil, fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 432 || resp.StatusCode == 433:
		return nil, ErrQuota
	case resp.StatusCode/100 != 2:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("search returned %d", resp.StatusCode)
	}
	var out struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("search decode: %w", err)
	}
	return out.Results, nil
}
