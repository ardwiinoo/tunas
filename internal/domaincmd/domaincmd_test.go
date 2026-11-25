package domaincmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type env struct {
	svc  *Service
	db   *store.DB
	dev  int64
	now  time.Time
	sent *[]Card
	fail *bool
}

func newEnv(t *testing.T) env {
	db := storetest.New(t)
	d, _, err := db.CreateDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	var sent []Card
	fail := false
	svc := &Service{DB: db, Now: func() time.Time { return now }, Loc: time.UTC,
		Send: func(_ context.Context, c Card) error {
			if fail {
				return errors.New("down")
			}
			sent = append(sent, c)
			return nil
		}}
	return env{svc, db, d.ID, now, &sent, &fail}
}

func (e env) add(t *testing.T, domain string, minutes int, day time.Time, hour int) {
	t.Helper()
	var bs []store.DomainBucket
	for i := 0; i < minutes; i++ {
		bs = append(bs, store.DomainBucket{Start: time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute), Domain: domain, ActiveSeconds: 60})
	}
	if _, err := e.db.UpsertDomainBuckets(context.Background(), e.dev, bs); err != nil {
		t.Fatal(err)
	}
}

func (e env) cmd(t *testing.T, args string) string {
	t.Helper()
	out, err := e.svc.Command(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSeedAndSuffixCategories(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if n, err := e.svc.Seed(ctx); err != nil || n < 10 {
		t.Fatalf("seed %d %v", n, err)
	}
	if n, _ := e.svc.Seed(ctx); n != 0 {
		t.Fatal("seeding twice adds nothing")
	}
	e.add(t, "m.youtube.com", 20, e.now, 9)
	e.add(t, "gist.github.com", 10, e.now, 11)
	e.add(t, "kompas.com", 5, e.now, 13)
	out := e.cmd(t, "")
	for _, want := range []string{"🌐 Situs hari ini", "m.youtube.com — 20m · hiburan", "gist.github.com — 10m · kerja", "? kompas.com — 5m · belum dikategorikan"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// the owner's choice wins over the seed, and applies to subdomains and the past
	if got := e.cmd(t, "youtube.com kerja"); !strings.Contains(got, "youtube.com (dan subdomainnya) masuk kategori kerja") {
		t.Fatalf("set: %q", got)
	}
	if out := e.cmd(t, ""); !strings.Contains(out, "m.youtube.com — 20m · kerja") {
		t.Fatalf("after the change:\n%s", out)
	}
	for _, bad := range []string{"https://youtube.com/x kerja", "youtube.com lucu", "localhost kerja"} {
		if got := e.cmd(t, bad); !strings.Contains(got, "Pakai: /domain") {
			t.Errorf("%q: %q", bad, got)
		}
	}
}

func TestBlockAndForgetAreByDomainAndSubdomains(t *testing.T) {
	e := newEnv(t)
	e.add(t, "bank.example.com", 10, e.now, 9)
	e.add(t, "example.com", 5, e.now, 10)
	e.add(t, "notexample.com", 7, e.now, 11)
	if out := e.cmd(t, "blok example.com"); !strings.Contains(out, "(15 catatan lama dihapus)") {
		t.Fatalf("block: %q", out)
	}
	if out := e.cmd(t, ""); strings.Contains(out, "bank.example.com") || !strings.Contains(out, "notexample.com") {
		t.Fatalf("after block:\n%s", out)
	}
	// the server drops it even if an old agent still sends it
	n, err := e.db.UpsertDomainBuckets(context.Background(), e.dev, []store.DomainBucket{
		{Start: e.now.Truncate(time.Hour), Domain: "bank.example.com", ActiveSeconds: 60},
		{Start: e.now.Truncate(time.Hour), Domain: "notexample.com", ActiveSeconds: 60},
	})
	if err != nil || n != 1 {
		t.Fatalf("stored %d (%v): a subdomain of a blocked domain must not be stored", n, err)
	}
	if out := e.cmd(t, "blok"); !strings.Contains(out, "example.com") {
		t.Errorf("list: %q", out)
	}
	if out := e.cmd(t, "buka example.com"); !strings.Contains(out, "boleh dicatat lagi") {
		t.Errorf("unblock: %q", out)
	}
	if out := e.cmd(t, "hapus notexample.com"); !strings.Contains(out, "Riwayat notexample.com dihapus (8 catatan)") {
		t.Errorf("forget: %q", out)
	}
	if out := e.cmd(t, "blok ../etc"); !strings.Contains(out, "Pakai: /domain") {
		t.Errorf("bad name: %q", out)
	}
}

func TestOnlyHostnamesAreInTheDatabase(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.add(t, "youtube.com", 3, e.now, 9)
	rows, err := e.db.Pool.Query(ctx, `select distinct domain from domain_buckets`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		if strings.ContainsAny(d, "/?# @:") {
			t.Errorf("stored %q", d)
		}
	}
}

func TestNoticeOncePerNewSite(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _ = e.svc.Seed(ctx)
	e.add(t, "youtube.com", 60, e.now, 9) // categorized by the seed: no question
	e.add(t, "kompas.com", 14, e.now, 9)
	notice := func(want int) {
		t.Helper()
		if err := e.svc.Notice(ctx); err != nil {
			t.Fatal(err)
		}
		if len(*e.sent) != want {
			t.Fatalf("%d notices, want %d: %+v", len(*e.sent), want, *e.sent)
		}
	}
	notice(0)
	e.add(t, "kompas.com", 31, e.now, 12) // 45 minutes in total
	notice(1)
	n := (*e.sent)[0]
	if n.Text != "Situs baru: kompas.com (45 mnt). Kategorinya?" || len(n.Rows) != 1 || len(n.Rows[0]) != 4 {
		t.Fatalf("notice %+v", n)
	}
	for _, b := range n.Rows[0] {
		if len(b.Data) > 64 {
			t.Errorf("callback data %q", b.Data)
		}
	}
	notice(1) // once
	c, ok, err := e.svc.Callback(ctx, n.Rows[0][0].Data)
	if err != nil || !ok || c.Text != "✅ kompas.com → kerja." {
		t.Fatalf("answer %+v %v %v", c, ok, err)
	}
	if cats, _ := e.db.DomainCategories(ctx); cats["kompas.com"] != "work" {
		t.Fatalf("stored %v", cats)
	}
	for _, junk := range []string{"dm:z:kompas.com", "dm:k:https://x.com/a", "dm:k", "dm:k:localhost"} {
		if c, ok, err := e.svc.Callback(ctx, junk); !ok || err != nil || c.Toast == "" {
			t.Errorf("%q: %+v %v %v", junk, c, ok, err)
		}
	}
	if _, ok, _ := e.svc.Callback(ctx, "ap:n"); ok {
		t.Error("another module's callback was taken")
	}
	// switched off, and a failed send is retried
	e.add(t, "twitch.example.net", 30, e.now, 15)
	_, _ = e.svc.Command(ctx, "notif off")
	notice(1)
	_, _ = e.svc.Command(ctx, "notif on")
	*e.fail = true
	if err := e.svc.Notice(ctx); err == nil {
		t.Fatal("the failure is reported")
	}
	*e.fail = false
	notice(2)
}

func TestSectionForToday(t *testing.T) {
	ts := []store.DomainTotal{{Domain: "github.com", Seconds: 3600}, {Domain: "youtube.com", Seconds: 600}}
	want := "Situs teratas:\n1. github.com — 1j\n2. youtube.com — 10m"
	if got := Section(ts, 5); got != want {
		t.Errorf("section %q", got)
	}
	if Section(nil, 5) != "" || strings.Count(Section(ts, 1), "\n") != 1 {
		t.Error("empty and limit")
	}
}
