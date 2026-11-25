package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/projects"
)

// secretTitle contains things that must never be stored: a file name and a
// path-like word. The project in it is "project-A".
const secretTitle = "project-A - Visual Studio Code"
const secretFile = "passwords-2026.txt"

type fakeTitles struct {
	mu    sync.Mutex
	proc  string
	title string
	err   error
}

func (f *fakeTitles) Foreground() (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.proc, f.title, f.err
}

func (f *fakeTitles) set(proc, title string) {
	f.mu.Lock()
	f.proc, f.title = proc, title
	f.mu.Unlock()
}

type projAPI struct {
	API
	mu   sync.Mutex
	got  [][]ProjectBucket
	fail bool
}

func (p *projAPI) PostProjects(_ context.Context, bs []ProjectBucket) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return context.DeadlineExceeded
	}
	p.got = append(p.got, append([]ProjectBucket(nil), bs...))
	return nil
}

func (p *projAPI) PostActivity(context.Context, []Bucket) error { return nil }

type projEnv struct {
	a      *Agent
	titles *fakeTitles
	api    *projAPI
	logs   *bytes.Buffer
	now    time.Time
	app    *string
	idle   *time.Duration
}

func newProjEnv(t *testing.T) *projEnv {
	logs := &bytes.Buffer{}
	e := &projEnv{titles: &fakeTitles{}, api: &projAPI{}, logs: logs,
		now: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	app, idle := "Code.exe", time.Duration(0)
	e.app, e.idle = &app, &idle
	ext, err := projects.NewExtractor(projects.DefaultEditors())
	if err != nil {
		t.Fatal(err)
	}
	e.a = &Agent{
		Cfg: testCfg(), Buf: openBuf(t), API: e.api, Extractor: ext, Titles: e.titles,
		Log:     slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Sampler: SamplerFunc(func() (Sample, error) { return Sample{App: *e.e().app, Idle: *e.e().idle}, nil }),
		Now:     func() time.Time { return e.now },
	}
	e.a.init()
	e.titles.set("Code.exe", secretTitle)
	return e
}

func (e *projEnv) e() *projEnv { return e }

func (e *projEnv) tick(t *testing.T) {
	t.Helper()
	if err := e.a.SampleOnce(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func (e *projEnv) pending(t *testing.T) []ProjectBucket {
	t.Helper()
	bs, err := e.a.Buf.PendingProjects(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

func TestProjectRecordingRules(t *testing.T) {
	e := newProjEnv(t)

	e.tick(t) // recording is off until the owner turns it on in the tray
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("recorded %d buckets while off", n)
	}

	e.a.SetProjects(true)
	e.tick(t)
	bs := e.pending(t)
	if len(bs) != 1 || bs[0].Project != "project-A" || bs[0].Editor != "code.exe" || bs[0].ActiveSeconds != 10 {
		t.Fatalf("buckets %+v", bs)
	}
	e.tick(t)
	if bs := e.pending(t); len(bs) != 1 || bs[0].ActiveSeconds != 20 {
		t.Fatalf("the minute bucket accumulates: %+v", bs)
	}

	// each of these records nothing
	for name, setup := range map[string]func(){
		"idle owner":                   func() { *e.idle = 5 * time.Minute },
		"another app in front":         func() { *e.app = "chrome.exe"; e.titles.set("chrome.exe", secretTitle) },
		"title does not match":         func() { e.titles.set("Code.exe", "Untitled-1 • Notepad") },
		"file name in title":           func() { e.titles.set("Code.exe", secretFile+" - project-A - Visual Studio Code") },
		"focus moved after the sample": func() { e.titles.set("slack.exe", "x") },
		"title read fails":             func() { e.titles.err = context.Canceled },
	} {
		e2 := newProjEnv(t)
		e2.a.SetProjects(true)
		e = e2
		setup()
		e.tick(t)
		if n := len(e.pending(t)); n != 0 {
			t.Errorf("%s: recorded %d buckets", name, n)
		}
	}

	// tracking paused stops project recording too
	e3 := newProjEnv(t)
	e3.a.SetProjects(true)
	e3.a.SetPaused(true)
	e3.tick(t)
	if n := len(e3.pending(t)); n != 0 {
		t.Fatalf("paused: recorded %d", n)
	}

	// the server's blocklist: never recorded, any letter case
	e4 := newProjEnv(t)
	e4.a.SetProjects(true)
	e4.a.setBlocklist([]string{"PROJECT-a"})
	e4.tick(t)
	if n := len(e4.pending(t)); n != 0 {
		t.Fatalf("blocked project recorded %d", n)
	}
	e4.a.setBlocklist(nil)
	e4.tick(t)
	if n := len(e4.pending(t)); n != 1 {
		t.Fatalf("unblocked: %d", n)
	}

	// no folder open
	e5 := newProjEnv(t)
	e5.a.SetProjects(true)
	e5.titles.set("Code.exe", "Visual Studio Code")
	e5.tick(t)
	if bs := e5.pending(t); len(bs) != 1 || bs[0].Project != projects.NoProject {
		t.Fatalf("no folder: %+v", bs)
	}
}

// The window title must not appear in the log (even at debug level), in the
// local database, or in what is sent to the server.
func TestWindowTitleNeverLeaves(t *testing.T) {
	e := newProjEnv(t)
	e.a.SetProjects(true)
	titles := []string{
		"● [Extension Development Host] project-A [SSH: prod-box] - Visual Studio Code",
		secretFile + " - project-A - Visual Studio Code", // ignored
		secretTitle,
		"Visual Studio Code",
		"totally unrelated title with a secret-token-123",
	}
	for i, title := range titles {
		e.titles.set("Code.exe", title)
		e.now = e.now.Add(time.Minute)
		e.tick(t)
		_ = i
	}
	if _, err := e.a.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// what was sent
	e.api.mu.Lock()
	payload, _ := json.Marshal(e.api.got)
	e.api.mu.Unlock()
	if !strings.Contains(string(payload), "project-A") {
		t.Fatalf("nothing was sent: %s", payload)
	}
	// the local database, every text column of every table
	dump := dumpBuffer(t, e.a.Buf)
	for _, hay := range map[string]string{"log": e.logs.String(), "payload": string(payload), "database": dump} {
		for _, secret := range []string{secretFile, "secret-token-123", "Extension Development Host", "SSH", "prod-box", "Visual Studio Code", "Untitled"} {
			if strings.Contains(hay, secret) {
				t.Errorf("%q leaked: %s", secret, firstLine(hay, secret))
			}
		}
	}
	// after a successful flush the buckets are marked synced and stay idempotent
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("%d buckets still pending", n)
	}
}

func firstLine(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func dumpBuffer(t *testing.T, b *Buffer) string {
	t.Helper()
	var sb strings.Builder
	rows, err := b.db.Query(`select name from sqlite_master where type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		r, err := b.db.Query(`select * from ` + tb)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			for _, v := range vals {
				switch x := v.(type) {
				case string:
					sb.WriteString(x + "|")
				case []byte:
					sb.WriteString(string(x) + "|")
				case sql.RawBytes:
					sb.WriteString(string(x) + "|")
				}
			}
			sb.WriteString("\n")
		}
		r.Close()
	}
	return sb.String()
}

// A failed upload keeps the buckets; the retry sends them again and the
// server's upsert makes that harmless.
func TestProjectBucketsResendUntilSynced(t *testing.T) {
	e := newProjEnv(t)
	e.a.SetProjects(true)
	e.tick(t)
	e.api.fail = true
	if _, err := e.a.flush(context.Background()); err == nil {
		t.Fatal("the upload error is reported")
	}
	if n := len(e.pending(t)); n != 1 {
		t.Fatalf("pending after a failed upload: %d", n)
	}
	e.api.fail = false
	if _, err := e.a.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pending(t)); n != 0 {
		t.Fatalf("pending after the retry: %d", n)
	}
	// time added after a sync makes the bucket dirty again and resends the whole minute
	e.tick(t)
	if bs := e.pending(t); len(bs) != 1 || bs[0].ActiveSeconds != 20 {
		t.Fatalf("resend %+v", bs)
	}
}
