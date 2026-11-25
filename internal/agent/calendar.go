package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The worklog calendar and slash commands of the laptop (PRD Fase 8d). The
// server decides what a day is; this file fetches it, keeps the last answer
// for offline use, and queues entries written while offline.

// CalDay is one day of a month.
type CalDay struct {
	D           int    `json:"d"`
	Count       int    `json:"count"`
	Kind        string `json:"kind"` // workday|weekend|holiday|leave|off|work
	HolidayName string `json:"holiday_name,omitempty"`
}

// CalMonth is GET /v1/agent/worklog/month.
type CalMonth struct {
	Month    string   `json:"month"`
	Today    string   `json:"today"`
	Days     []CalDay `json:"days"`
	Workdays int      `json:"workdays"`
	Filled   int      `json:"filled"`
}

// CalEntry is one entry of a day.
type CalEntry struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// CalDayView is GET /v1/agent/worklog/day.
type CalDayView struct {
	Date        string     `json:"date"`
	Kind        string     `json:"kind"`
	HolidayName string     `json:"holiday_name,omitempty"`
	Entries     []CalEntry `json:"entries"`
}

// CommandResult is POST /v1/agent/command.
type CommandResult struct {
	Kind string `json:"kind"` // text | local | unsupported
	Text string `json:"text"`
}

// CalMonth fetches a month ("YYYY-MM").
func (c *Client) CalMonth(ctx context.Context, month string) (CalMonth, error) {
	var out CalMonth
	err := c.do(ctx, http.MethodGet, "/v1/agent/worklog/month?m="+url.QueryEscape(month), nil, &out)
	return out, err
}

// CalDay fetches a day ("YYYY-MM-DD").
func (c *Client) CalDay(ctx context.Context, date string) (CalDayView, error) {
	var out CalDayView
	err := c.do(ctx, http.MethodGet, "/v1/agent/worklog/day?d="+url.QueryEscape(date), nil, &out)
	return out, err
}

// EditEntry replaces an entry's text.
func (c *Client) EditEntry(ctx context.Context, id int64, text string) error {
	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/v1/agent/worklog/entries/%d", id), map[string]string{"text": text}, nil)
}

// DeleteEntry removes an entry.
func (c *Client) DeleteEntry(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/v1/agent/worklog/entries/%d", id), nil, nil)
}

// RunCommand runs a whitelisted slash command on the server.
func (c *Client) RunCommand(ctx context.Context, cmd, args string) (CommandResult, error) {
	var out CommandResult
	err := c.do(ctx, http.MethodPost, "/v1/agent/command", map[string]string{"cmd": cmd, "args": args}, &out)
	return out, err
}

// CalAPI is the part of the client the calendar needs (faked in tests).
type CalAPI interface {
	CalMonth(ctx context.Context, month string) (CalMonth, error)
	CalDay(ctx context.Context, date string) (CalDayView, error)
	EditEntry(ctx context.Context, id int64, text string) error
	DeleteEntry(ctx context.Context, id int64) error
}

// Cache sizes: the last months and days that were looked at.
const (
	maxCachedMonths = 6
	maxCachedDays   = 40
)

type cacheEntry struct {
	Saved time.Time       `json:"saved"`
	Data  json.RawMessage `json:"data"`
}

// CalCache keeps the last answers on disk so the calendar still opens when
// the server cannot be reached. Entries are the same data the server sent
// (worklog text the owner wrote); the file is private to the user.
type CalCache struct {
	Path string
	Now  func() time.Time

	mu sync.Mutex
}

func (c *CalCache) load() map[string]cacheEntry {
	m := map[string]cacheEntry{}
	if c.Path == "" {
		return m
	}
	if raw, err := os.ReadFile(c.Path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func (c *CalCache) save(m map[string]cacheEntry) {
	if c.Path == "" {
		return
	}
	// keep the newest few of each kind
	for _, prefix := range []string{"m:", "d:"} {
		limit := maxCachedMonths
		if prefix == "d:" {
			limit = maxCachedDays
		}
		var keys []string
		for k := range m {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]].Saved.After(m[keys[j]].Saved) })
		for _, k := range keys[min(limit, len(keys)):] {
			delete(m, k)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	tmp := c.Path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, c.Path)
	}
}

func (c *CalCache) put(key string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.load()
	m[key] = cacheEntry{Saved: c.Now(), Data: raw}
	c.save(m)
}

func (c *CalCache) get(key string, dst any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.load()[key]
	return ok && json.Unmarshal(e.Data, dst) == nil
}

// forget drops cached days and months that an edit made out of date.
func (c *CalCache) forget(prefixes ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.load()
	for k := range m {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				delete(m, k)
			}
		}
	}
	c.save(m)
}

// Calendar is what the overlay's calendar card uses: the server first, the
// cache when the server cannot be reached ("data lama").
type Calendar struct {
	API   CalAPI
	Cache *CalCache
}

// ErrOffline is returned for an edit or delete while the server is unreachable.
var ErrOffline = errors.New("server tidak terjangkau")

// reachable reports whether err means "the server did not answer" (so a
// cached copy is better than nothing); an answer such as 400 or 404 is not.
func reachable(err error) bool {
	var se *StatusError
	return !errors.As(err, &se) || se.Code >= 500
}

// Month returns the month; stale=true when it comes from the cache.
func (c *Calendar) Month(ctx context.Context, month string) (m CalMonth, stale bool, err error) {
	m, err = c.API.CalMonth(ctx, month)
	if err == nil {
		c.Cache.put("m:"+month, m)
		return m, false, nil
	}
	if reachable(err) && c.Cache.get("m:"+month, &m) {
		return m, true, nil
	}
	return CalMonth{}, false, err
}

// Day returns a day; stale=true when it comes from the cache.
func (c *Calendar) Day(ctx context.Context, date string) (v CalDayView, stale bool, err error) {
	v, err = c.API.CalDay(ctx, date)
	if err == nil {
		c.Cache.put("d:"+date, v)
		return v, false, nil
	}
	if reachable(err) && c.Cache.get("d:"+date, &v) {
		return v, true, nil
	}
	return CalDayView{}, false, err
}

// Edit changes an entry. It needs the server (an offline edit would have to
// be merged later); the caller shows ErrOffline.
func (c *Calendar) Edit(ctx context.Context, id int64, text string) error {
	err := c.API.EditEntry(ctx, id, text)
	if err != nil && reachable(err) {
		return ErrOffline
	}
	c.Cache.forget("d:", "m:")
	return err
}

// Delete removes an entry (server needed, like Edit).
func (c *Calendar) Delete(ctx context.Context, id int64) error {
	err := c.API.DeleteEntry(ctx, id)
	if err != nil && reachable(err) {
		return ErrOffline
	}
	c.Cache.forget("d:", "m:")
	return err
}

// ForgetCached drops cached calendar data (after an entry was added).
func (c *Calendar) ForgetCached() { c.Cache.forget("d:", "m:") }

// PlainText makes server text drawable in the bubble: the embedded font has
// no emoji or symbols beyond a few, so ✅ becomes √, ⚠️ becomes "!" and other
// emoji are dropped.
func PlainText(s string) string {
	s = strings.NewReplacer("✅", "√", "⚠️", "!", "❌", "x", "↩️", "", "➕", "+").Replace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0xFE0F || r == 0x200D: // variation selector, joiner
		case r >= 0x1F000 && r <= 0x1FAFF, r >= 0x2600 && r <= 0x27BF, r >= 0x2B00 && r <= 0x2BFF:
		case r == '\n' || r == '\t' || !unicode.IsControl(r):
			b.WriteRune(r)
		}
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "  ") { // the space an emoji left behind
			l = l[1:]
		}
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
