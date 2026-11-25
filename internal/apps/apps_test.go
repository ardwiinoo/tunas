package apps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type env struct {
	svc   *Service
	db    *store.DB
	dev   int64
	now   *time.Time
	sent  *[]Card
	fail  *bool
	start time.Time
}

func newEnv(t *testing.T) env {
	db := storetest.New(t)
	ctx := context.Background()
	d, _, err := db.CreateDevice(ctx, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	var sent []Card
	fail := false
	svc := &Service{DB: db, Now: func() time.Time { return now }, Loc: time.UTC,
		Send: func(_ context.Context, c Card) error {
			if fail {
				return errors.New("telegram down")
			}
			sent = append(sent, c)
			return nil
		}}
	return env{svc, db, d.ID, &now, &sent, &fail, time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)}
}

// use adds minutes of active time for an app, starting at the given hour.
func (e env) use(t *testing.T, app, storedCat string, minutes int, at time.Time) {
	t.Helper()
	var bs []store.Bucket
	for i := 0; i < minutes; i++ {
		bs = append(bs, store.Bucket{Start: at.Add(time.Duration(i) * time.Minute), App: app, Category: storedCat, ActiveSeconds: 60})
	}
	if err := e.db.UpsertBuckets(context.Background(), e.dev, bs); err != nil {
		t.Fatal(err)
	}
}

func TestListShowsUncategorizedFirstWithButtons(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.use(t, "Code.exe", "work", 120, e.start)
	e.use(t, "Antigravity.exe", "other", 45, e.start.Add(3*time.Hour))
	if _, err := e.svc.Seed(ctx, map[string]string{"code.exe": "work"}); err != nil {
		t.Fatal(err)
	}
	c, err := e.svc.List(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(c.Text, "? Antigravity.exe — 45 m · belum dikategorikan"), strings.Index(c.Text, "Code.exe — 2 j · kerja")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("an uncategorized app comes first, marked with ?:\n%s", c.Text)
	}
	if len(c.Rows) != 4 || len(c.Rows[1]) != 4 || c.Rows[1][0].Text != "Kerja" || c.Rows[1][3].Text != "Lainnya" {
		t.Fatalf("each app has a name row and four category buttons: %+v", c.Rows)
	}
	if c.Rows[3][0].Text != "✔ Kerja" {
		t.Errorf("the current category is marked: %+v", c.Rows[3])
	}
	for _, row := range c.Rows {
		for _, b := range row {
			if len(b.Data) > 64 {
				t.Errorf("callback data %q is over 64 bytes", b.Data)
			}
		}
	}
	// nothing used today → a plain message
	empty := newEnv(t)
	if c, _ := empty.svc.List(ctx, "h"); c.Rows != nil || !strings.Contains(c.Text, "Belum ada aktivitas") {
		t.Errorf("empty: %+v", c)
	}
}

// The mapping ignores letter case, and a change applies to the past (/today).
func TestCategoryChangeAppliesBackwards(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.use(t, "Antigravity.exe", "other", 30, e.start)
	day := func() activity.Summary {
		from, to := activity.DayRange(*e.now, time.UTC)
		bs, err := e.db.BucketsBetween(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		return activity.Summarize(bs)
	}
	if s := day(); s.ByCategory["work"] != 0 || s.ByCategory["other"] != 30*time.Minute {
		t.Fatalf("before: %v", s.ByCategory)
	}
	for _, in := range []struct{ cmd, args string }{{"kategori", "ANTIGRAVITY.EXE Kerja"}, {"kategori", "antigravity kerja"}, {"kategori", "Antigravity.exe work"}} {
		c, ok, err := e.svc.Command(ctx, in.cmd, in.args)
		if err != nil || !ok || !strings.Contains(c.Text, "antigravity.exe sekarang masuk kategori kerja") {
			t.Fatalf("%q: %v %v %q", in.args, ok, err, c.Text)
		}
	}
	if s := day(); s.ByCategory["work"] != 30*time.Minute || s.ByCategory["other"] != 0 {
		t.Fatalf("after: %v", s.ByCategory)
	}
	// and back again
	if _, _, err := e.svc.Command(ctx, "kategori", "Antigravity.exe hiburan"); err != nil {
		t.Fatal(err)
	}
	if s := day(); s.ByCategory["distract"] != 30*time.Minute {
		t.Fatalf("changed again: %v", s.ByCategory)
	}
	// an app with no mapping keeps the category the agent stored
	e.use(t, "Foo.exe", "browse", 5, e.start.Add(5*time.Hour))
	if s := day(); s.ByCategory["browse"] != 5*time.Minute {
		t.Fatalf("stored category: %v", s.ByCategory)
	}
}

func TestKategoriCommandInputs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, bad := range []string{"", "Antigravity.exe", "Antigravity.exe lucu", "a/b.exe kerja", `c:\x\y.exe kerja`, strings.Repeat("x", 200) + " kerja"} {
		c, ok, err := e.svc.Command(ctx, "kategori", bad)
		if err != nil || !ok || !strings.Contains(c.Text, "Pakai: /kategori") {
			t.Errorf("%q: %q", bad, c.Text)
		}
	}
	if m, _ := e.db.AppCategories(ctx); len(m) != 0 {
		t.Fatalf("a bad command stored %v", m)
	}
	// a name with a space, and the Indonesian words
	for in, want := range map[string]string{"Docker Desktop.exe hiburan": "distract", "Code.exe jelajah": "browse", "Zed lainnya": "other"} {
		if _, _, err := e.svc.Command(ctx, "kategori", in); err != nil {
			t.Fatal(err)
		}
		app, _ := NormalizeApp(strings.TrimSuffix(in, in[strings.LastIndex(in, " "):]))
		if m, _ := e.db.AppCategories(ctx); m[app] != want {
			t.Errorf("%q → %v, want %s", in, m, want)
		}
	}
	if c, ok, _ := e.svc.Command(ctx, "ping", ""); ok || c.Text != "" {
		t.Error("other commands are not taken")
	}
}

func TestCallbacksChangeAndRefresh(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.use(t, "Antigravity.exe", "other", 45, e.start)
	c, err := e.svc.List(ctx, "h")
	if err != nil {
		t.Fatal(err)
	}
	data := c.Rows[1][0].Data // Kerja
	got, ok, err := e.svc.Callback(ctx, data)
	if err != nil || !ok || got.Toast != "antigravity.exe → kerja" || !strings.Contains(got.Text, "Antigravity.exe — 45 m · kerja") || strings.Contains(got.Text, "\n? ") {
		t.Fatalf("callback %q: %v %v %+v", data, ok, err, got)
	}
	if m, _ := e.db.AppCategories(ctx); m["antigravity.exe"] != "work" {
		t.Fatalf("stored %v", m)
	}
	// the name label does nothing, junk is refused without storing
	if c, ok, _ := e.svc.Callback(ctx, "ap:n"); !ok || c.Text != "" || c.Toast != "" {
		t.Errorf("label: %+v", c)
	}
	for _, junk := range []string{"ap:h:z:foo.exe", "ap:h:k:../x", "ap:bad", "ap:h:k:"} {
		if c, ok, err := e.svc.Callback(ctx, junk); !ok || err != nil || c.Toast == "" {
			t.Errorf("%q: %+v %v %v", junk, c, ok, err)
		}
	}
	if _, ok, _ := e.svc.Callback(ctx, "st:r"); ok {
		t.Error("another module's callback was taken")
	}
}

func TestNoticeOncePerNewApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.use(t, "Antigravity.exe", "other", 14, e.start) // 14 min: not yet
	e.use(t, "Code.exe", "work", 200, e.start)
	if _, err := e.svc.Seed(ctx, map[string]string{"code.exe": "work"}); err != nil {
		t.Fatal(err)
	}
	notice := func(want int) {
		t.Helper()
		if err := e.svc.Notice(ctx); err != nil {
			t.Fatal(err)
		}
		if len(*e.sent) != want {
			t.Fatalf("%d notices, want %d: %+v", len(*e.sent), want, *e.sent)
		}
	}
	notice(0) // under 15 minutes; a categorized app never counts
	e.use(t, "Antigravity.exe", "other", 31, e.start.Add(time.Hour))
	notice(1)
	n := (*e.sent)[0]
	if n.Text != "Aplikasi baru: Antigravity.exe (45 mnt). Kategorinya?" || len(n.Rows) != 1 || len(n.Rows[0]) != 4 {
		t.Fatalf("notice %+v", n)
	}
	notice(1) // once per app, not every five minutes
	*e.now = e.now.Add(24 * time.Hour)
	notice(1)

	// pressing a button categorizes it and replaces the question
	c, ok, err := e.svc.Callback(ctx, n.Rows[0][0].Data)
	if err != nil || !ok || c.Text != "✅ antigravity.exe → kerja." || c.Rows != nil {
		t.Fatalf("answer %+v %v %v", c, ok, err)
	}

	// a second new app gets its own notice; the switch turns them off
	e.use(t, "Zed.exe", "other", 20, e.start.Add(4*time.Hour))
	if _, _, err := e.svc.Command(ctx, "apps", "notif off"); err != nil {
		t.Fatal(err)
	}
	notice(1)
	if _, _, err := e.svc.Command(ctx, "apps", "notif on"); err != nil {
		t.Fatal(err)
	}
	notice(2)
	if !strings.Contains((*e.sent)[1].Text, "Zed.exe (20 mnt)") {
		t.Fatalf("second notice %q", (*e.sent)[1].Text)
	}
}

// A notice that could not be sent is tried again, and is not counted as sent.
func TestNoticeRetriesAfterSendFailure(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.use(t, "Zed.exe", "other", 30, e.start)
	*e.fail = true
	if err := e.svc.Notice(ctx); err == nil {
		t.Fatal("the failure is reported")
	}
	*e.fail = false
	if err := e.svc.Notice(ctx); err != nil || len(*e.sent) != 1 {
		t.Fatalf("retry: %v %d", err, len(*e.sent))
	}
}

func TestNormalizeApp(t *testing.T) {
	for in, want := range map[string]string{"Code.exe": "code.exe", "  Antigravity ": "antigravity.exe", "Docker Desktop.exe": "docker desktop.exe", "idea64": "idea64.exe"} {
		if got, ok := NormalizeApp(in); !ok || got != want {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "a/b", `a\b.exe`, "x;y.exe", "x\ny.exe", strings.Repeat("a", 101)} {
		if got, ok := NormalizeApp(bad); ok {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
}
