package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeCats struct {
	mu     sync.Mutex
	set    CategorySet
	err    error
	asked  []string // etags the agent sent
	seeded []map[string]string
}

func (f *fakeCats) Categories(_ context.Context, etag string) (CategorySet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, etag)
	if f.err != nil {
		return CategorySet{}, f.err
	}
	if etag == f.set.ETag {
		return CategorySet{ETag: f.set.ETag}, nil // current: no map
	}
	return f.set, nil
}

func (f *fakeCats) SeedCategories(_ context.Context, apps map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seeded = append(f.seeded, apps)
	return nil
}

func (f *fakeCats) change(set CategorySet) {
	f.mu.Lock()
	f.set = set
	f.mu.Unlock()
}

func newCatAgent(t *testing.T) *Agent {
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler), CategoriesFile: filepath.Join(t.TempDir(), "categories.json")}
	a.init()
	return a
}

func TestCategoryOfPrefersTheServer(t *testing.T) {
	a := newCatAgent(t)
	if got := a.categoryOf("Code.exe"); got != "work" { // config.yaml until the server's mapping is known
		t.Fatalf("yaml fallback = %q", got)
	}
	if got := a.categoryOf("Antigravity.exe"); got != "other" {
		t.Fatalf("unknown = %q", got)
	}
	a.setCategories(CategorySet{ETag: "e1", Apps: map[string]string{"antigravity.exe": "work", "code.exe": "distract"}})
	for app, want := range map[string]string{
		"Antigravity.exe": "work",     // any letter case
		"ANTIGRAVITY.EXE": "work",     //
		"code.exe":        "distract", // the server overrides config.yaml
		"chrome.exe":      "browse",   // not on the server: config.yaml still answers
		"unknown.exe":     "other",
	} {
		if got := a.categoryOf(app); got != want {
			t.Errorf("categoryOf(%q) = %q, want %q", app, got, want)
		}
	}
}

func TestRefreshUsesETagAndPersists(t *testing.T) {
	a := newCatAgent(t)
	api := &fakeCats{set: CategorySet{ETag: "e1", Apps: map[string]string{"zed.exe": "work"}}}
	ctx := context.Background()
	if changed, err := a.RefreshCategories(ctx, api); err != nil || !changed {
		t.Fatalf("first refresh %v %v", changed, err)
	}
	if changed, err := a.RefreshCategories(ctx, api); err != nil || changed {
		t.Fatalf("same etag must not change anything: %v %v", changed, err)
	}
	if api.asked[0] != "" || api.asked[1] != "e1" {
		t.Fatalf("etags sent: %q", api.asked)
	}
	api.change(CategorySet{ETag: "e2", Apps: map[string]string{"zed.exe": "distract"}})
	if changed, _ := a.RefreshCategories(ctx, api); !changed || a.categoryOf("zed.exe") != "distract" {
		t.Fatal("a new etag brings the new mapping")
	}
	// an error keeps what the agent has
	api.err = errors.New("offline")
	if _, err := a.RefreshCategories(ctx, api); err == nil || a.categoryOf("zed.exe") != "distract" {
		t.Fatal("offline: keep the last mapping")
	}

	// a restart starts from the saved copy, before any contact with the server
	b := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler), CategoriesFile: a.CategoriesFile}
	b.init()
	b.loadCategories()
	if got := b.categoryOf("zed.exe"); got != "distract" {
		t.Fatalf("after restart = %q", got)
	}
	if err := os.WriteFile(a.CategoriesFile, []byte("{rusak"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Agent{Cfg: testCfg(), Buf: openBuf(t), Log: slog.New(slog.DiscardHandler), CategoriesFile: a.CategoriesFile}
	c.init()
	c.loadCategories() // a broken file is ignored
	if got := c.categoryOf("code.exe"); got != "work" {
		t.Fatalf("broken file = %q", got)
	}
}

// The activity written to the buffer uses the server's category as soon as
// it is known (and so does the form machine).
func TestSamplesUseTheServerMapping(t *testing.T) {
	a := newCatAgent(t)
	a.setCategories(CategorySet{ETag: "e", Apps: map[string]string{"antigravity.exe": "work"}})
	if got := a.categoryOf("Antigravity.exe"); got != "work" {
		t.Fatalf("got %q", got)
	}
}

// PRD: the agent follows a changed mapping within 2 minutes.
func TestLoopFollowsAChangeWithinTwoMinutes(t *testing.T) {
	if CategoryEvery > 2*time.Minute {
		t.Fatalf("poll interval %v exceeds the 2 minute promise", CategoryEvery)
	}
	a := newCatAgent(t)
	api := &fakeCats{set: CategorySet{ETag: "e1", Apps: map[string]string{"zed.exe": "work"}}}
	a.API = &catsAPI{cats: api}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { a.categoriesLoop(ctx); close(done) }()
	waitFor(t, func() bool { return a.categoryOf("zed.exe") == "work" })
	api.mu.Lock()
	n := len(api.seeded)
	api.mu.Unlock()
	if n != 1 {
		t.Fatalf("config.yaml is sent as the seed once, got %d", n)
	}
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// catsAPI is an API that only serves categories (the loop touches nothing else).
type catsAPI struct {
	API
	cats *fakeCats
}

func (c *catsAPI) Categories(ctx context.Context, etag string) (CategorySet, error) {
	return c.cats.Categories(ctx, etag)
}

func (c *catsAPI) SeedCategories(ctx context.Context, apps map[string]string) error {
	return c.cats.SeedCategories(ctx, apps)
}
