package api

import (
	"context"
	"testing"
	"time"
)

func TestStateCategoriesETag(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.db.SetAppCategory(ctx, "Code.exe", "work", time.Now()); err != nil {
		t.Fatal(err)
	}
	code, out := h.do("GET", "/v1/agent/state", h.token, nil)
	etag, _ := out["categories_etag"].(string)
	apps, _ := out["app_categories"].(map[string]any)
	if code != 200 || etag == "" || apps["code.exe"] != "work" {
		t.Fatalf("first state = %d %v", code, out)
	}
	// with the current etag the map is left out, but the rest of the state stays
	code, out = h.do("GET", "/v1/agent/state?categories_etag="+etag, h.token, nil)
	if code != 200 || out["app_categories"] != nil || out["categories_etag"] != etag || out["now"] == nil {
		t.Fatalf("same etag = %d %v", code, out)
	}
	// a change from Telegram changes the etag and sends the map again
	if err := h.db.SetAppCategory(ctx, "Antigravity.exe", "work", time.Now()); err != nil {
		t.Fatal(err)
	}
	code, out = h.do("GET", "/v1/agent/state?categories_etag="+etag, h.token, nil)
	apps, _ = out["app_categories"].(map[string]any)
	if code != 200 || out["categories_etag"] == etag || apps["antigravity.exe"] != "work" || len(apps) != 2 {
		t.Fatalf("after a change = %d %v", code, out)
	}
	// re-saving the same mapping does not change the etag
	e2 := out["categories_etag"]
	_ = h.db.SetAppCategory(ctx, "ANTIGRAVITY.EXE", "work", time.Now())
	if _, out = h.do("GET", "/v1/agent/state", h.token, nil); out["categories_etag"] != e2 {
		t.Fatal("the etag follows the content, not the update time")
	}
}

func TestSeedCategoriesFromTheAgent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.db.SetAppCategory(ctx, "spotify.exe", "other", time.Now()) // an owner's choice
	code, out := h.do("POST", "/v1/agent/categories/seed", h.token, map[string]any{"apps": map[string]string{
		"Code.exe": "work", "Spotify.exe": "distract", "Foo Bar.exe": "browse"}})
	if code != 200 || out["added"] != float64(2) {
		t.Fatalf("seed = %d %v", code, out)
	}
	m, _ := h.db.AppCategories(ctx)
	if m["code.exe"] != "work" || m["foo bar.exe"] != "browse" || m["spotify.exe"] != "other" {
		t.Fatalf("mapping %v (an existing choice must not be overwritten)", m)
	}
	for name, body := range map[string]any{
		"bad category": map[string]any{"apps": map[string]string{"a.exe": "fun"}},
		"path":         map[string]any{"apps": map[string]string{`C:\x\a.exe`: "work"}},
		"slash":        map[string]any{"apps": map[string]string{"a/b.exe": "work"}},
		"empty name":   map[string]any{"apps": map[string]string{"": "work"}},
	} {
		if code, _ := h.do("POST", "/v1/agent/categories/seed", h.token, body); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	big := map[string]string{}
	for i := 0; i < maxSeedApps+1; i++ {
		big["app"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))+".exe"] = "work"
	}
	if code, _ := h.do("POST", "/v1/agent/categories/seed", h.token, map[string]any{"apps": big}); code != 400 {
		t.Errorf("too many = %d", code)
	}
	if code, _ := h.do("POST", "/v1/agent/categories/seed", "", map[string]any{"apps": map[string]string{"a.exe": "work"}}); code != 401 {
		t.Errorf("no token = %d", code)
	}
}
