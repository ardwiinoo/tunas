package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func projBucket(h *harness, project, editor string, sec int) map[string]any {
	return map[string]any{"bucket_start": h.now.Truncate(time.Minute).Format(time.RFC3339), "project": project, "editor": editor, "active_seconds": sec}
}

func TestPostProjectsStoresNamesOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	code, out := h.do("POST", "/v1/agent/projects", h.token, map[string]any{"buckets": []any{
		projBucket(h, "project-A", "code.exe", 30), projBucket(h, "(tanpa proyek)", "code.exe", 10)}})
	if code != 200 || out["accepted"] != float64(2) {
		t.Fatalf("post = %d %v", code, out)
	}
	// resending the same minute replaces it
	if code, _ := h.do("POST", "/v1/agent/projects", h.token, map[string]any{"buckets": []any{projBucket(h, "project-A", "code.exe", 45)}}); code != 200 {
		t.Fatal(code)
	}
	var secs int
	if err := h.db.Pool.QueryRow(ctx, `select active_seconds from project_buckets where project = 'project-A'`).Scan(&secs); err != nil || secs != 45 {
		t.Fatalf("stored %d (%v)", secs, err)
	}
	if code, _ := h.do("POST", "/v1/agent/projects", "", map[string]any{"buckets": []any{}}); code != 401 {
		t.Errorf("no token = %d", code)
	}

	// anything that is not a plain project name is refused outright
	for name, b := range map[string]map[string]any{
		"path":            projBucket(h, `C:\Users\me\secret.txt`, "code.exe", 5),
		"slash":           projBucket(h, "a/b", "code.exe", 5),
		"title with file": projBucket(h, strings.Repeat("secret document title ", 8), "code.exe", 5),
		"newline":         projBucket(h, "a\nb", "code.exe", 5),
		"empty":           projBucket(h, "", "code.exe", 5),
		"padded":          projBucket(h, "  project-A ", "code.exe", 5),
		"editor path":     projBucket(h, "p", `C:\x\Code.exe`, 5),
		"editor caps":     projBucket(h, "p", "Code.exe", 5),
		"too many secs":   projBucket(h, "p", "code.exe", 61),
		"negative secs":   projBucket(h, "p", "code.exe", -1),
		"not aligned":     {"bucket_start": h.now.Add(time.Second).Format(time.RFC3339), "project": "p", "editor": "code.exe", "active_seconds": 5},
		"too old":         {"bucket_start": h.now.Add(-400 * 24 * time.Hour).Truncate(time.Minute).Format(time.RFC3339), "project": "p", "editor": "code.exe", "active_seconds": 5},
	} {
		if code, _ := h.do("POST", "/v1/agent/projects", h.token, map[string]any{"buckets": []any{b}}); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	var n int
	_ = h.db.Pool.QueryRow(ctx, `select count(*) from project_buckets`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d rows after the refused uploads, want 2", n)
	}
}

func TestStateCarriesTheProjectBlocklist(t *testing.T) {
	h := newHarness(t)
	_, _ = h.db.BlockProject(context.Background(), "Secret-Client")
	code, out := h.do("GET", "/v1/agent/state?categories_etag=anything", h.token, nil)
	bl, _ := out["project_blocklist"].([]any)
	if code != 200 || len(bl) != 1 || bl[0] != "secret-client" {
		t.Fatalf("state = %d %v", code, out)
	}
	// even with a current categories etag the blocklist is still sent
	etag, _ := out["categories_etag"].(string)
	_, out = h.do("GET", "/v1/agent/state?categories_etag="+etag, h.token, nil)
	if out["app_categories"] != nil || out["project_blocklist"] == nil {
		t.Fatalf("same etag: %v", out)
	}
	// nothing blocked: an empty list, not null
	h2 := newHarness(t)
	_, out = h2.do("GET", "/v1/agent/state", h2.token, nil)
	if l, ok := out["project_blocklist"].([]any); !ok || len(l) != 0 {
		t.Fatalf("empty blocklist = %v", out["project_blocklist"])
	}
}
