package ghbackup

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

const secret = "github_pat_TESTTOKEN_must_never_be_logged"

// fakeGitHub is an in-memory Contents API for one repo.
type fakeGitHub struct {
	mu       sync.Mutex
	files    map[string]string // path → content
	fail     int               // fail the next n requests with 500
	messages []string
	auth     []string
	puts     int
}

func blobSHA(s string) string {
	h := sha1.Sum([]byte("blob " + s))
	return hex.EncodeToString(h[:])
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.fail > 0 {
		f.fail--
		w.WriteHeader(500)
		return
	}
	const prefix = "/repos/owner/backup/contents"
	p := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, prefix), "/")
	switch r.Method {
	case http.MethodGet:
		if p == "" || p == "logs" { // directory listing
			var out []map[string]string
			for name := range f.files {
				dir, base := filepath.Split(name)
				if strings.TrimSuffix(dir, "/") == p {
					out = append(out, map[string]string{"name": base, "type": "file"})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		c, ok := f.files[p]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": filepath.Base(p), "sha": blobSHA(c),
			"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(c))})
	case http.MethodPut:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		cur, exists := f.files[p]
		if (exists && body["sha"] != blobSHA(cur)) || (!exists && body["sha"] != "") {
			w.WriteHeader(409)
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(body["content"])
		f.files[p] = string(raw)
		f.messages = append(f.messages, body["message"])
		f.puts++
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]string{"sha": blobSHA(string(raw))}})
	}
}

type rig struct {
	t     *testing.T
	gh    *fakeGitHub
	db    *store.DB
	w     *Worker
	now   time.Time
	logs  *bytes.Buffer
	notes []string
}

func newRig(t *testing.T, dir string) *rig {
	g := &rig{t: t, gh: &fakeGitHub{files: map[string]string{}}, db: storetest.New(t), logs: &bytes.Buffer{},
		now: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(g.gh)
	t.Cleanup(srv.Close)
	c, err := NewClient(secret, "owner/backup", "main", dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Base = srv.URL
	g.w = &Worker{GH: c, DB: g.db, Now: func() time.Time { return g.now }, Log: slog.New(slog.NewTextHandler(g.logs, nil)),
		Notify: func(_ context.Context, s string) error { g.notes = append(g.notes, s); return nil }}
	return g
}

func (g *rig) add(day, text string) store.WorklogEntry {
	g.t.Helper()
	d, _ := time.Parse(time.DateOnly, day)
	e, _, err := g.db.AddWorklog(context.Background(), d, text, "telegram")
	if err != nil {
		g.t.Fatal(err)
	}
	return e
}

func (g *rig) pass() map[string]Result {
	g.t.Helper()
	res, err := g.w.Pass(context.Background(), false, false)
	if err != nil {
		g.t.Fatal(err)
	}
	return res
}

func TestBackupPushDebounceAndUpdate(t *testing.T) {
	g := newRig(t, "")
	g.add("2026-11-17", "rapat sprint")
	g.add("2026-11-18", "review PR")
	g.now = time.Now() // changed_at is the DB clock
	if r := g.pass()["2026-11"]; r != Waiting {
		t.Fatalf("within 60 s: %s", r)
	}
	g.now = time.Now().Add(61 * time.Second)
	if r := g.pass()["2026-11"]; r != Pushed {
		t.Fatalf("after the quiet period: %s", r)
	}
	want := "17/11/2026\n* rapat sprint\n\n18/11/2026\n* review PR\n"
	if g.gh.files["202611_daily.txt"] != want {
		t.Fatalf("file:\n%q", g.gh.files["202611_daily.txt"])
	}
	if g.gh.messages[0] != "worklog: 2026-11 (2 entri)" {
		t.Fatalf("commit message %q (no entry text allowed)", g.gh.messages[0])
	}
	if len(g.pass()) != 0 {
		t.Fatal("nothing dirty after a push")
	}
	// an edit is pushed with the last sha (update, not create)
	e := g.add("2026-11-18", "deploy")
	if _, err := g.db.UpdateWorklog(context.Background(), e.ID, "deploy staging", time.Now()); err != nil {
		t.Fatal(err)
	}
	g.now = time.Now().Add(61 * time.Second)
	if r := g.pass()["2026-11"]; r != Pushed || !strings.Contains(g.gh.files["202611_daily.txt"], "* deploy staging\n") {
		t.Fatalf("update: %s\n%s", r, g.gh.files["202611_daily.txt"])
	}
	for _, a := range g.gh.auth {
		if a != "Bearer "+secret {
			t.Fatal("token goes in the Authorization header")
		}
	}
	if strings.Contains(g.logs.String(), secret) {
		t.Fatal("token leaked into the log")
	}
}

func TestBackupConflictNeverOverwrites(t *testing.T) {
	g := newRig(t, "logs")
	g.add("2026-11-18", "pertama")
	g.now = time.Now().Add(61 * time.Second)
	g.pass()
	// someone edits the file in the repo (e.g. the old app still syncing)
	external := "18/11/2026\n* pertama\n* ditulis aplikasi lain\n"
	g.gh.files["logs/202611_daily.txt"] = external
	g.add("2026-11-18", "kedua")
	g.now = time.Now().Add(61 * time.Second)
	if r := g.pass()["2026-11"]; r != Conflict {
		t.Fatalf("external change: %s", r)
	}
	if g.gh.files["logs/202611_daily.txt"] != external {
		t.Fatal("a conflicting file must not be overwritten")
	}
	st, _ := g.db.SyncState(context.Background(), "2026-11")
	if st.Status != "conflict" || len(g.notes) != 1 || !strings.Contains(g.notes[0], "/worklog sync force") {
		t.Fatalf("status %s, notes %q", st.Status, g.notes)
	}
	g.pass()
	if len(g.notes) != 1 || g.gh.puts != 1 {
		t.Fatal("conflict waits for the owner: no retry, no repeated alert")
	}
	// /worklog sync force: Tunas's version wins
	if out := g.w.SyncNow(context.Background(), "2026-11", true); !strings.Contains(out, "terkirim") {
		t.Fatalf("force: %q", out)
	}
	if !strings.Contains(g.gh.files["logs/202611_daily.txt"], "* kedua") {
		t.Fatal("force pushes the Tunas version")
	}
}

func TestBackupStaleShaAndBackoff(t *testing.T) {
	g := newRig(t, "")
	g.add("2026-11-18", "satu")
	g.now = time.Now().Add(61 * time.Second)
	g.pass()
	// our stored sha goes stale but the content is still ours: retry with the remote sha
	_ = g.db.SyncOK(context.Background(), "2026-11", "0000stale", hashOf(g.gh.files["202611_daily.txt"]), time.Now(), time.Now())
	g.add("2026-11-18", "dua")
	g.now = time.Now().Add(61 * time.Second)
	if r := g.pass()["2026-11"]; r != Pushed {
		t.Fatalf("stale sha with unchanged remote: %s", r)
	}

	// GitHub down: backoff 1, 5, 30 min, then error + one alert
	g.add("2026-11-18", "tiga")
	g.gh.fail = 100
	g.now = time.Now().Add(61 * time.Second)
	start := g.now
	steps := []struct {
		after time.Duration
		want  Result
	}{
		{0, Failed}, {30 * time.Second, Waiting}, {time.Minute, Failed}, {4 * time.Minute, Waiting},
		{5 * time.Minute, Failed}, {20 * time.Minute, Waiting},
	}
	for i, s := range steps {
		g.now = g.now.Add(s.after)
		if r := g.pass()["2026-11"]; r != s.want {
			t.Fatalf("step %d (+%s): %s, want %s", i, g.now.Sub(start), r, s.want)
		}
	}
	st, _ := g.db.SyncState(context.Background(), "2026-11")
	if st.Status != "error" || len(g.notes) != 1 || !strings.Contains(g.notes[0], "gagal 3 kali") {
		t.Fatalf("after 3 failures: %s %q", st.Status, g.notes)
	}
	g.gh.fail = 0
	g.now = g.now.Add(31 * time.Minute)
	if r := g.pass()["2026-11"]; r != Pushed {
		t.Fatalf("recovers: %s", r)
	}
	if strings.Contains(g.logs.String(), secret) {
		t.Fatal("token leaked into the log")
	}
}

func TestImport(t *testing.T) {
	g := newRig(t, "logs")
	fixtures, _ := filepath.Glob(filepath.Join("..", "worklog", "testdata", "*_daily.txt"))
	for _, f := range fixtures {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		g.gh.files["logs/"+filepath.Base(f)] = string(b)
	}
	g.gh.files["logs/README.md"] = "bukan berkas worklog"
	ctx := context.Background()

	var out bytes.Buffer
	reps, err := Import(ctx, g.w.GH, g.db, true, time.Now(), &out)
	if err != nil {
		t.Fatal(err)
	}
	counts, _ := g.db.WorklogCounts(ctx, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(counts) != 0 || len(reps) != 3 {
		t.Fatalf("dry run wrote %d days, %d reports", len(counts), len(reps))
	}
	if !strings.Contains(out.String(), "202611_daily.txt: 4 hari, 7 entri (7 baru, 0 sudah ada)") {
		t.Fatalf("dry run summary:\n%s", out.String())
	}
	if strings.Contains(out.String(), "menyiapkan lingkungan") {
		t.Fatal("the summary must not print entries")
	}

	out.Reset()
	reps, err = Import(ctx, g.w.GH, g.db, false, time.Now(), &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		if r.Entries > 0 && !r.SameRender {
			t.Errorf("%s: render after import differs from the file", r.Name)
		}
	}
	// idempotent
	out.Reset()
	reps, _ = Import(ctx, g.w.GH, g.db, false, time.Now(), &out)
	for _, r := range reps {
		if r.New != 0 {
			t.Errorf("%s: second import inserted %d", r.Name, r.New)
		}
	}
	// after import nothing is pushed, and a new entry updates the same file
	g.now = time.Now().Add(time.Hour)
	if res := g.pass(); len(res) != 0 {
		t.Fatalf("imported months are in sync: %v", res)
	}
	g.add("2026-11-23", "entri baru dari Tunas")
	g.now = time.Now().Add(61 * time.Second)
	if r := g.pass()["2026-11"]; r != Pushed {
		t.Fatalf("push after import: %s", r)
	}
	f := g.gh.files["logs/202611_daily.txt"]
	if !strings.HasSuffix(f, "\n\n23/11/2026\n* entri baru dari Tunas\n") || !strings.HasPrefix(f, "02/11/2026\n") {
		t.Fatalf("new day appended like the old app:\n%s", f)
	}
}
