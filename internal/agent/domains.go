package agent

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/domains"
)

// Website time (PRD Fase 8c). The agent never reads an address bar: the
// owner's browser extension tells the local bridge the hostname of the active
// tab, and the agent counts it while a known browser is in front.

// StaleTab is how long a signal from before the browser got the focus stays
// usable (PRD: a site with no signal for 15 s after a switch is dropped). The
// extension reports on every focus or tab change, so a signal from after the
// focus is valid for as long as nothing replaces it.
const StaleTab = 15 * time.Second

// DomainsAPI is the optional part of the client that uploads website time.
type DomainsAPI interface {
	PostDomains(ctx context.Context, bs []DomainBucket) error
}

// PostDomains uploads domain buckets (idempotent).
func (c *Client) PostDomains(ctx context.Context, bs []DomainBucket) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/domains", map[string]any{"buckets": bs}, nil)
}

type domainState struct {
	mu         sync.Mutex
	on         bool
	browsers   map[string]bool // lower-case process names
	blocked    []string
	cats       map[string]string // domain → category (for the distraction nudge)
	cur        string            // the last host the extension reported ("" = none)
	curAt      time.Time
	inBrowser  bool
	focusSince time.Time // when the browser came to the front (at the latest: the sample that saw it)
	prevAt     time.Time // the previous sample
}

// SetBrowsers sets the processes whose active tab counts.
func (a *Agent) SetBrowsers(names []string) {
	a.dom.mu.Lock()
	defer a.dom.mu.Unlock()
	a.dom.browsers = map[string]bool{}
	for _, n := range names {
		a.dom.browsers[strings.ToLower(n)] = true
	}
}

// SetDomains turns website recording on or off (the tray's "Catat domain situs").
func (a *Agent) SetDomains(on bool) {
	a.dom.mu.Lock()
	a.dom.on = on
	if !on {
		a.dom.cur, a.dom.inBrowser = "", false
	}
	a.dom.mu.Unlock()
}

// DomainsOn reports whether website recording is on.
func (a *Agent) DomainsOn() bool {
	a.dom.mu.Lock()
	defer a.dom.mu.Unlock()
	return a.dom.on
}

// OnTab is called by the bridge with the hostname of the active tab ("" when
// there is none). It re-checks the hostname: the agent does not trust the
// bridge to have validated it.
func (a *Agent) OnTab(host string, at time.Time) {
	if host != "" {
		h, ok := domains.Normalize(host)
		if !ok {
			return
		}
		host = h
	}
	a.dom.mu.Lock()
	a.dom.cur, a.dom.curAt = host, at
	a.dom.mu.Unlock()
}

func (a *Agent) setDomainRules(blocked []string, cats map[string]string) {
	a.dom.mu.Lock()
	defer a.dom.mu.Unlock()
	if blocked != nil {
		a.dom.blocked = blocked
	}
	if cats != nil {
		a.dom.cats = cats
	}
}

func (a *Agent) domainBlocked(host string) bool {
	for _, b := range a.dom.blocked {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

// currentDomain is the website in front of the owner right now ("" if none):
// a known browser is the foreground app, the extension said which host, and
// that signal is not stale. It also tracks when the browser got the focus.
func (a *Agent) currentDomain(now time.Time, app string) string {
	a.dom.mu.Lock()
	defer a.dom.mu.Unlock()
	prev := a.dom.prevAt
	a.dom.prevAt = now
	if !a.dom.on || !a.dom.browsers[strings.ToLower(app)] {
		a.dom.inBrowser = false
		return ""
	}
	if !a.dom.inBrowser {
		// the focus moved to the browser some time since the previous sample
		a.dom.inBrowser, a.dom.focusSince = true, time.Time{} // first sample ever: whatever was reported since start counts
		if !prev.IsZero() {
			a.dom.focusSince = prev
		}
	}
	if a.dom.cur == "" {
		return ""
	}
	sinceFocus := !a.dom.curAt.Before(a.dom.focusSince)
	if !sinceFocus && now.Sub(a.dom.focusSince) > StaleTab { // (a zero focusSince never gets here)
		return "" // nothing was reported since the browser came to the front
	}
	if a.domainBlocked(a.dom.cur) {
		return ""
	}
	return a.dom.cur
}

// recordDomain adds this sample to the website in front of the owner.
func (a *Agent) recordDomain(ctx context.Context, now time.Time, app string, secs int) string {
	host := a.currentDomain(now, app)
	if host == "" {
		return ""
	}
	if err := a.Buf.AddDomain(ctx, now, host, secs); err != nil {
		a.Log.Warn("website time not buffered", "err", err)
	}
	return host
}

// flushDomains uploads pending domain buckets in batches.
func (a *Agent) flushDomains(ctx context.Context) error {
	api, ok := a.API.(DomainsAPI)
	if !ok {
		return nil
	}
	for {
		bs, err := a.Buf.PendingDomains(ctx, syncBatch)
		if err != nil || len(bs) == 0 {
			return err
		}
		if err := api.PostDomains(ctx, bs); err != nil {
			return err
		}
		if err := a.Buf.MarkDomainsSynced(ctx, bs); err != nil {
			return err
		}
		if _, err := a.Buf.PruneDomains(ctx, a.Now().Truncate(time.Minute).Add(-time.Minute)); err != nil {
			return err
		}
		if len(bs) < syncBatch {
			return nil
		}
	}
}
