package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func domBucket(h *harness, domain string, sec int) map[string]any {
	return map[string]any{"bucket_start": h.now.Truncate(time.Minute).Format(time.RFC3339), "domain": domain, "active_seconds": sec}
}

func TestPostDomainsStoresHostnamesOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if code, out := h.do("POST", "/v1/agent/domains", h.token, map[string]any{"buckets": []any{domBucket(h, "youtube.com", 30), domBucket(h, "m.youtube.com", 10)}}); code != 200 || out["accepted"] != float64(2) {
		t.Fatalf("post = %d %v", code, out)
	}
	// a resent minute is replaced
	h.do("POST", "/v1/agent/domains", h.token, map[string]any{"buckets": []any{domBucket(h, "youtube.com", 45)}})
	var secs int
	if err := h.db.Pool.QueryRow(ctx, `select active_seconds from domain_buckets where domain = 'youtube.com'`).Scan(&secs); err != nil || secs != 45 {
		t.Fatalf("stored %d (%v)", secs, err)
	}
	for name, d := range map[string]string{
		"url":         "https://youtube.com/watch?v=abc",
		"path":        "youtube.com/watch",
		"query":       "youtube.com?x=1",
		"credentials": "user:pass@youtube.com",
		"port":        "youtube.com:8080",
		"ip":          "192.168.1.5",
		"localhost":   "localhost",
		"upper case":  "YouTube.com",
		"www":         "www.youtube.com",
		"title":       "Video lucu - YouTube",
		"idn":         "bücher.example",
		"empty":       "",
		"too long":    strings.Repeat("a", 300) + ".com",
	} {
		if code, _ := h.do("POST", "/v1/agent/domains", h.token, map[string]any{"buckets": []any{domBucket(h, d, 5)}}); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := h.do("POST", "/v1/agent/domains", h.token, map[string]any{"buckets": []any{domBucket(h, "a.com", 61)}}); code != 400 {
		t.Error("seconds over 60")
	}
	if code, _ := h.do("POST", "/v1/agent/domains", "", map[string]any{"buckets": []any{}}); code != 401 {
		t.Error("no token")
	}
	var n int
	_ = h.db.Pool.QueryRow(ctx, `select count(*) from domain_buckets`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d rows after the refused uploads, want 2", n)
	}
}

func TestStateCarriesDomainRulesAndTegurSwitch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.db.SetDomainCategory(ctx, "youtube.com", "distract", time.Now())
	_, _ = h.db.BlockDomain(ctx, "bank.example.com")
	code, out := h.do("GET", "/v1/agent/state", h.token, nil)
	dc, _ := out["domain_categories"].(map[string]any)
	db, _ := out["domain_blocklist"].([]any)
	if code != 200 || dc["youtube.com"] != "distract" || len(db) != 1 || db[0] != "bank.example.com" || out["tegur_enabled"] != true {
		t.Fatalf("state = %d %v", code, out)
	}
	etag, _ := out["categories_etag"].(string)
	// the etag also follows the domain categories
	_ = h.db.SetDomainCategory(ctx, "tiktok.com", "distract", time.Now())
	_, out2 := h.do("GET", "/v1/agent/state?categories_etag="+etag, h.token, nil)
	if out2["categories_etag"] == etag || out2["domain_categories"] == nil {
		t.Fatalf("a changed domain category must change the etag: %v", out2)
	}
	// with the current etag the maps are left out, the blocklist and the switch are not
	_, out3 := h.do("GET", "/v1/agent/state?categories_etag="+out2["categories_etag"].(string), h.token, nil)
	if out3["domain_categories"] != nil || out3["app_categories"] != nil || out3["domain_blocklist"] == nil || out3["tegur_enabled"] == nil {
		t.Fatalf("current etag: %v", out3)
	}
	_ = h.db.SetSetting(ctx, TegurEnabledKey, false)
	if _, o := h.do("GET", "/v1/agent/state", h.token, nil); o["tegur_enabled"] != false {
		t.Fatalf("tegur off: %v", o["tegur_enabled"])
	}
}
