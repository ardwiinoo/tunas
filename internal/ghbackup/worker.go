package ghbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/worklog"
)

// Repo is the GitHub side (Client implements it).
type Repo interface {
	Get(ctx context.Context, name string) (File, error)
	Put(ctx context.Context, name, content, sha, message string) (string, error)
	List(ctx context.Context) ([]string, error)
}

// Worker pushes dirty months after a quiet period.
type Worker struct {
	GH       Repo
	DB       *store.DB
	Now      func() time.Time
	Log      *slog.Logger
	Notify   func(ctx context.Context, text string) error
	Debounce time.Duration // default 60 s

	mu      sync.Mutex
	retries map[string]*retry
	alerted map[string]time.Time
}

type retry struct {
	failures int
	next     time.Time
}

// Backoff after the 1st, 2nd, 3rd failure; from the 3rd on the month is
// in error and the owner is told (once an hour).
var Backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// AlertCooldown limits repeated warnings of one kind.
const AlertCooldown = time.Hour

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MonthContent renders a month from the database in the file format.
func MonthContent(ctx context.Context, db *store.DB, month time.Time) (string, int, error) {
	es, err := db.WorklogMonth(ctx, month)
	if err != nil {
		return "", 0, err
	}
	var days []worklog.Day
	for _, e := range es {
		if len(days) == 0 || !days[len(days)-1].Date.Equal(e.Date) {
			days = append(days, worklog.Day{Date: e.Date})
		}
		days[len(days)-1].Bullets = append(days[len(days)-1].Bullets, e.Text)
	}
	return worklog.Render(days), len(es), nil
}

// Result of one month's sync.
type Result string

const (
	Pushed    Result = "pushed"
	Unchanged Result = "unchanged"
	Conflict  Result = "conflict"
	Failed    Result = "failed"
	Waiting   Result = "waiting" // debounce or backoff
)

// Pass syncs every dirty month. force pushes now, ignoring the debounce,
// the backoff and conflicts (it overwrites the repo file with Tunas's
// version: /worklog sync force).
func (w *Worker) Pass(ctx context.Context, now bool, force bool) (map[string]Result, error) {
	states, err := w.DB.SyncStates(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Result{}
	for _, st := range states {
		if !st.Dirty && !(force && st.Status != "ok") {
			continue
		}
		if st.Status == "conflict" && !force {
			out[st.Month] = Conflict
			continue
		}
		if !now && !force {
			if w.Now().Sub(st.ChangedAt) < w.debounce() || w.backingOff(st.Month) {
				out[st.Month] = Waiting
				continue
			}
		}
		out[st.Month] = w.syncMonth(ctx, st, force)
	}
	return out, nil
}

func (w *Worker) debounce() time.Duration {
	if w.Debounce > 0 {
		return w.Debounce
	}
	return 60 * time.Second
}

func (w *Worker) backingOff(month string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.retries[month]
	return r != nil && w.Now().Before(r.next)
}

func (w *Worker) syncMonth(ctx context.Context, st store.WorklogSync, force bool) Result {
	month, err := time.Parse("2006-01", st.Month)
	if err != nil {
		return Failed
	}
	content, n, err := MonthContent(ctx, w.DB, month)
	if err != nil {
		return w.fail(ctx, st.Month, err)
	}
	name := worklog.FileName(month)
	hash := hashOf(content)
	if !force && st.Status == "ok" && deref(st.LastContentHash) == hash {
		return w.ok(ctx, st, deref(st.LastSHA), hash, Unchanged)
	}
	sha := deref(st.LastSHA)
	if sha == "" || force {
		// first push, or forced: see what is in the repo now
		f, err := w.GH.Get(ctx, name)
		switch {
		case errors.Is(err, ErrNotFound):
			sha = ""
		case err != nil:
			return w.fail(ctx, st.Month, err)
		case f.Content == content:
			return w.ok(ctx, st, f.SHA, hash, Unchanged)
		case !force && hashOf(f.Content) != deref(st.LastContentHash):
			return w.conflict(ctx, st.Month) // someone else's file: never overwrite
		default:
			sha = f.SHA
		}
	}
	msg := fmt.Sprintf("worklog: %s (%d entri)", st.Month, n) // no entry text in the commit
	newSHA, err := w.GH.Put(ctx, name, content, sha, msg)
	if errors.Is(err, ErrConflict) {
		// the sha we had is stale: only safe when the remote content is
		// still what we pushed last time
		f, gerr := w.GH.Get(ctx, name)
		if gerr != nil {
			return w.fail(ctx, st.Month, gerr)
		}
		if !force && hashOf(f.Content) != deref(st.LastContentHash) {
			return w.conflict(ctx, st.Month)
		}
		newSHA, err = w.GH.Put(ctx, name, content, f.SHA, msg)
	}
	if err != nil {
		if errors.Is(err, ErrConflict) && !force {
			return w.conflict(ctx, st.Month)
		}
		return w.fail(ctx, st.Month, err)
	}
	w.Log.Info("worklog backed up", "month", st.Month, "entries", n)
	return w.ok(ctx, st, newSHA, hash, Pushed)
}

func (w *Worker) ok(ctx context.Context, st store.WorklogSync, sha, hash string, r Result) Result {
	if err := w.DB.SyncOK(ctx, st.Month, sha, hash, st.ChangedAt, w.Now()); err != nil {
		w.Log.Warn("backup state not saved", "month", st.Month, "err", err)
		return Failed
	}
	w.mu.Lock()
	delete(w.retries, st.Month)
	w.mu.Unlock()
	return r
}

func (w *Worker) conflict(ctx context.Context, month string) Result {
	w.Log.Warn("worklog backup conflict: the repo file changed outside Tunas", "month", month)
	_ = w.DB.SyncFailed(ctx, month, "conflict", "file changed outside Tunas")
	w.alert(ctx, "conflict:"+month, fmt.Sprintf("⚠️ Berkas worklog %s di GitHub diubah di luar Tunas, jadi tidak kutimpa. "+
		"Cek repo-nya; bila versi Tunas yang benar, kirim /worklog sync force.", month))
	return Conflict
}

func (w *Worker) fail(ctx context.Context, month string, err error) Result {
	w.mu.Lock()
	if w.retries == nil {
		w.retries = map[string]*retry{}
	}
	r := w.retries[month]
	if r == nil {
		r = &retry{}
		w.retries[month] = r
	}
	r.failures++
	r.next = w.Now().Add(Backoff[min(r.failures, len(Backoff))-1])
	failures := r.failures
	w.mu.Unlock()
	w.Log.Warn("worklog backup failed", "month", month, "failures", failures, "err", err)
	if failures >= len(Backoff) {
		_ = w.DB.SyncFailed(ctx, month, "error", err.Error())
		w.alert(ctx, "error", fmt.Sprintf("⚠️ Cadangan worklog ke GitHub gagal %d kali (%s). Cek GITHUB_BACKUP_TOKEN/REPO; "+
			"catatan tetap aman di database dan akan dicoba lagi.", failures, month))
	}
	return Failed
}

func (w *Worker) alert(ctx context.Context, kind, text string) {
	w.mu.Lock()
	if w.alerted == nil {
		w.alerted = map[string]time.Time{}
	}
	if last, ok := w.alerted[kind]; ok && w.Now().Sub(last) < AlertCooldown {
		w.mu.Unlock()
		return
	}
	w.alerted[kind] = w.Now()
	w.mu.Unlock()
	if w.Notify != nil {
		if err := w.Notify(ctx, text); err != nil {
			w.Log.Warn("backup alert not sent", "err", err)
		}
	}
}

// Run passes every 15 s until ctx is done.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if _, err := w.Pass(ctx, false, false); err != nil && ctx.Err() == nil {
			w.Log.Warn("backup pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SyncNow is /worklog sync [force]: marks the current month, pushes now
// and words the outcome.
func (w *Worker) SyncNow(ctx context.Context, month string, force bool) string {
	if err := w.DB.MarkMonthDirty(ctx, month); err != nil {
		return "Maaf, gagal membaca status cadangan."
	}
	res, err := w.Pass(ctx, true, force)
	if err != nil {
		return "Maaf, gagal membaca status cadangan."
	}
	if len(res) == 0 {
		return "☁️ Cadangan worklog sudah terbaru."
	}
	var b strings.Builder
	b.WriteString("☁️ Cadangan worklog:")
	words := map[Result]string{Pushed: "terkirim", Unchanged: "sudah sama", Conflict: "KONFLIK (diubah di luar Tunas; /worklog sync force untuk menimpa)",
		Failed: "gagal, dicoba lagi otomatis", Waiting: "menunggu"}
	months := make([]string, 0, len(res))
	for m := range res {
		months = append(months, m)
	}
	sort.Strings(months)
	for _, m := range months {
		fmt.Fprintf(&b, "\n• %s: %s", m, words[res[m]])
	}
	return b.String()
}
