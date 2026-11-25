package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// CategoryEvery is how often the agent asks the server whether the app →
// category mapping changed (PRD 8b: follow a change within 2 minutes).
const CategoryEvery = 60 * time.Second

// CategorySet is the server's mapping with its etag. Apps is nil when the
// etag the agent sent is still current.
type CategorySet struct {
	ETag      string
	Apps      map[string]string // lower-case app → category
	Blocklist []string          // projects that are never recorded (always sent)

	DomainCategories map[string]string // website → category (only when the etag changed)
	DomainBlocklist  []string          // websites that are never recorded (always sent)
	TegurEnabled     *bool             // /tegur on|off (always sent)
}

// CategoryAPI is the optional part of the client that serves the mapping.
type CategoryAPI interface {
	Categories(ctx context.Context, etag string) (CategorySet, error)
	SeedCategories(ctx context.Context, apps map[string]string) error
}

// Categories asks GET /v1/agent/state for the mapping; the server leaves the
// map out when etag is current.
func (c *Client) Categories(ctx context.Context, etag string) (CategorySet, error) {
	var out struct {
		ETag      string            `json:"categories_etag"`
		Apps      map[string]string `json:"app_categories"`
		Blocklist []string          `json:"project_blocklist"`

		DomainCategories map[string]string `json:"domain_categories"`
		DomainBlocklist  []string          `json:"domain_blocklist"`
		TegurEnabled     *bool             `json:"tegur_enabled"`
	}
	path := "/v1/agent/state"
	if etag != "" {
		path += "?categories_etag=" + url.QueryEscape(etag)
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return CategorySet{ETag: out.ETag, Apps: out.Apps, Blocklist: out.Blocklist, DomainCategories: out.DomainCategories,
		DomainBlocklist: out.DomainBlocklist, TegurEnabled: out.TegurEnabled}, err
}

// SeedCategories sends the apps of config.yaml; the server adds the ones it
// has no category for and never overwrites a choice made from Telegram.
func (c *Client) SeedCategories(ctx context.Context, apps map[string]string) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/categories/seed", map[string]any{"apps": apps}, nil)
}

// categoryState is the mapping the agent uses.
type categoryState struct {
	mu   sync.Mutex
	etag string
	apps map[string]string // nil until the server's mapping (or the saved copy) is known
}

// categoryOf is the category of an app: the server's mapping first, then
// config.yaml (the initial seed, and the answer while no mapping is known).
func (a *Agent) categoryOf(app string) string {
	a.cats.mu.Lock()
	cat, ok := a.cats.apps[strings.ToLower(app)]
	a.cats.mu.Unlock()
	if ok {
		return cat
	}
	return a.Cfg.CategoryOf(app)
}

func (a *Agent) setCategories(set CategorySet) {
	a.cats.mu.Lock()
	a.cats.etag, a.cats.apps = set.ETag, set.Apps
	a.cats.mu.Unlock()
}

type savedCategories struct {
	ETag    string            `json:"etag"`
	Apps    map[string]string `json:"apps"`
	Domains map[string]string `json:"domains,omitempty"`
}

// loadCategories reads the copy saved by the last run, so the form machine
// has the owner's mapping before the first contact with the server.
func (a *Agent) loadCategories() {
	if a.CategoriesFile == "" {
		return
	}
	raw, err := os.ReadFile(a.CategoriesFile)
	if err != nil {
		return
	}
	var s savedCategories
	if json.Unmarshal(raw, &s) == nil && s.Apps != nil {
		a.setCategories(CategorySet{ETag: s.ETag, Apps: s.Apps})
		a.setDomainRules(nil, s.Domains)
	}
}

func (a *Agent) saveCategories(set CategorySet) {
	if a.CategoriesFile == "" {
		return
	}
	raw, _ := json.Marshal(savedCategories{ETag: set.ETag, Apps: set.Apps, Domains: set.DomainCategories})
	tmp := a.CategoriesFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, a.CategoriesFile)
	}
}

// seedApps is config.yaml's mapping as the server stores it.
func (a *Agent) seedApps() map[string]string {
	out := map[string]string{}
	for cat, apps := range a.Cfg.AppCategories {
		for _, app := range apps {
			out[strings.ToLower(app)] = cat
		}
	}
	return out
}

// RefreshCategories fetches the mapping once; it reports whether it changed.
func (a *Agent) RefreshCategories(ctx context.Context, api CategoryAPI) (bool, error) {
	a.cats.mu.Lock()
	etag := a.cats.etag
	a.cats.mu.Unlock()
	set, err := api.Categories(ctx, etag)
	if err != nil {
		return false, err
	}
	a.setBlocklist(set.Blocklist)
	a.setDomainRules(set.DomainBlocklist, set.DomainCategories)
	if set.TegurEnabled != nil {
		a.setTegurEnabled(*set.TegurEnabled)
	}
	if set.Apps == nil || set.ETag == etag {
		return false, nil
	}
	a.setCategories(set)
	a.saveCategories(set)
	return true, nil
}

func (a *Agent) categoriesLoop(ctx context.Context) {
	api, ok := a.API.(CategoryAPI)
	if !ok {
		return
	}
	a.loadCategories()
	seeded := false
	for {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if !seeded {
			if err := api.SeedCategories(cctx, a.seedApps()); err == nil {
				seeded = true
			} else {
				a.Log.Debug("category seed not sent yet", "err", err)
			}
		}
		changed, err := a.RefreshCategories(cctx, api)
		cancel()
		switch {
		case err != nil:
			a.Log.Debug("categories not refreshed", "err", err)
		case changed:
			a.Log.Info("app categories updated from the server")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(CategoryEvery):
		}
	}
}
