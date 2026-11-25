package agent

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/projects"
)

// ProjectBucket is one minute of editor time on one project (wire format).
// It holds the extracted project name and the editor's process name, never a
// window title.
type ProjectBucket struct {
	Start         time.Time `json:"bucket_start"`
	Project       string    `json:"project"`
	Editor        string    `json:"editor"`
	ActiveSeconds int       `json:"active_seconds"`
}

// ProjectsAPI is the optional part of the client that uploads project time.
type ProjectsAPI interface {
	PostProjects(ctx context.Context, bs []ProjectBucket) error
}

// PostProjects uploads project buckets (idempotent: a resent bucket replaces
// the stored one).
func (c *Client) PostProjects(ctx context.Context, bs []ProjectBucket) error {
	return c.do(ctx, http.MethodPost, "/v1/agent/projects", map[string]any{"buckets": bs}, nil)
}

// projectState is what recording keeps between samples.
type projectState struct {
	mu      sync.Mutex
	on      bool
	blocked map[string]bool // projects.Key → never recorded (from the server)
}

// SetProjects turns project recording on or off (the tray's "Catat proyek
// editor"). It is off until the owner turns it on.
func (a *Agent) SetProjects(on bool) {
	a.proj.mu.Lock()
	a.proj.on = on
	a.proj.mu.Unlock()
}

// ProjectsOn reports whether project recording is on.
func (a *Agent) ProjectsOn() bool {
	a.proj.mu.Lock()
	defer a.proj.mu.Unlock()
	return a.proj.on
}

func (a *Agent) setBlocklist(names []string) {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[projects.Key(n)] = true
	}
	a.proj.mu.Lock()
	a.proj.blocked = m
	a.proj.mu.Unlock()
}

func (a *Agent) blocked(name string) bool {
	a.proj.mu.Lock()
	defer a.proj.mu.Unlock()
	return a.proj.blocked[projects.Key(name)]
}

// recordProject adds this sample to the project the foreground editor is
// about. The window title lives only in this function: it is matched and
// dropped, and nothing here logs it. Nothing is recorded unless recording is
// on, tracking is not paused, the owner is not idle and the foreground
// window belongs to an allowed editor with a title that matches its pattern.
func (a *Agent) recordProject(ctx context.Context, now time.Time, app string, secs int) {
	if !a.ProjectsOn() || a.Extractor == nil || a.Titles == nil || !a.Extractor.Allowed(app) {
		return
	}
	proc, title, err := a.Titles.Foreground()
	if err != nil || !strings.EqualFold(proc, app) { // the focus moved since the sample
		return
	}
	name, ok := a.Extractor.Project(proc, title) // the title is not used after this line
	if !ok || a.blocked(name) {
		return
	}
	if err := a.Buf.AddProject(ctx, now, name, strings.ToLower(proc), secs); err != nil {
		a.Log.Warn("project time not buffered", "err", err)
	}
}

// flushProjects uploads pending project buckets in batches.
func (a *Agent) flushProjects(ctx context.Context) error {
	api, ok := a.API.(ProjectsAPI)
	if !ok {
		return nil
	}
	for {
		bs, err := a.Buf.PendingProjects(ctx, syncBatch)
		if err != nil || len(bs) == 0 {
			return err
		}
		if err := api.PostProjects(ctx, bs); err != nil {
			return err
		}
		if err := a.Buf.MarkProjectsSynced(ctx, bs); err != nil {
			return err
		}
		if _, err := a.Buf.PruneProjects(ctx, a.Now().Truncate(time.Minute).Add(-time.Minute)); err != nil {
			return err
		}
		if len(bs) < syncBatch {
			return nil
		}
	}
}
