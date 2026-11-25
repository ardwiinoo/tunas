package projreport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

type env struct {
	svc *Service
	db  *store.DB
	dev int64
	now time.Time
}

func newEnv(t *testing.T) env {
	db := storetest.New(t)
	d, _, err := db.CreateDevice(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	return env{&Service{DB: db, Now: func() time.Time { return now }, Loc: time.UTC}, db, d.ID, now}
}

// add records minutes of a project in an editor, starting at the given hour of the day.
func (e env) add(t *testing.T, project, editor string, minutes int, day time.Time, hour int) {
	t.Helper()
	var bs []store.ProjectBucket
	for i := 0; i < minutes; i++ {
		bs = append(bs, store.ProjectBucket{Start: time.Date(day.Year(), day.Month(), day.Day(), hour, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute),
			Project: project, Editor: editor, ActiveSeconds: 60})
	}
	if _, err := e.db.UpsertProjectBuckets(context.Background(), e.dev, bs); err != nil {
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

func TestCompactAndEditorLabel(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "<1m", 20 * time.Second: "<1m", 45 * time.Minute: "45m", 2 * time.Hour: "2j", 130 * time.Minute: "2j10m", 59*time.Minute + 40*time.Second: "1j"} {
		if got := Compact(d); got != want {
			t.Errorf("Compact(%v) = %q, want %q", d, got, want)
		}
	}
	for in, want := range map[string]string{"Code.exe": "VS Code", "antigravity.exe": "Antigravity", "zed.exe": "Zed", "Rider64.exe": "Rider64"} {
		if got := EditorLabel(in); got != want {
			t.Errorf("EditorLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProyekReportShowsTimeAndEditors(t *testing.T) {
	e := newEnv(t)
	day := e.now
	e.add(t, "project-A", "code.exe", 130, day, 8)
	e.add(t, "project-B", "antigravity.exe", 45, day, 12)
	e.add(t, "Project-A", "antigravity.exe", 5, day, 14) // other letter case, other editor: the same project
	out := e.cmd(t, "")
	for _, want := range []string{"💻 Proyek hari ini — 3j", "• project-A 2j15m (VS Code, Antigravity)", "• project-B 45m (Antigravity)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "project-A") > strings.Index(out, "project-B") {
		t.Error("most time first")
	}
	// yesterday is not in "hari", but is in "minggu" and "bulan"
	e.add(t, "project-C", "code.exe", 60, day.AddDate(0, 0, -1), 9)
	if strings.Contains(e.cmd(t, "hari"), "project-C") || !strings.Contains(e.cmd(t, "minggu"), "project-C") || !strings.Contains(e.cmd(t, "bulan"), "project-C") {
		t.Error("ranges")
	}
	if got := e.cmd(t, "tahun"); !strings.Contains(got, "Pakai: /proyek") {
		t.Errorf("unknown word: %q", got)
	}
	empty := newEnv(t)
	if got := empty.cmd(t, ""); !strings.Contains(got, "Belum ada waktu proyek") {
		t.Errorf("empty: %q", got)
	}
}

// An alias renames a project, merges projects that share it, across
// editors, and applies to days already recorded.
func TestAliasMergesAcrossEditorsAndAppliesBackwards(t *testing.T) {
	e := newEnv(t)
	day := e.now
	e.add(t, "client-x", "code.exe", 60, day.AddDate(0, 0, -2), 9) // two days ago
	e.add(t, "x-internal", "antigravity.exe", 30, day, 9)
	if out := e.cmd(t, "minggu"); !strings.Contains(out, "client-x 1j (VS Code)") || !strings.Contains(out, "x-internal 30m (Antigravity)") {
		t.Fatalf("before:\n%s", out)
	}
	for _, args := range []string{`alias client-x "Proyek X"`, `alias X-INTERNAL Proyek X`} {
		if out := e.cmd(t, args); !strings.Contains(out, "sekarang tampil sebagai Proyek X") {
			t.Fatalf("%s: %q", args, out)
		}
	}
	out := e.cmd(t, "minggu")
	if !strings.Contains(out, "• Proyek X 1j30m (VS Code, Antigravity)") || strings.Contains(out, "client-x") || strings.Contains(out, "x-internal") {
		t.Fatalf("after:\n%s", out)
	}
	if got := e.cmd(t, "alias"); !strings.Contains(got, "client-x → Proyek X") {
		t.Errorf("alias list: %q", got)
	}
	if got := e.cmd(t, "alias client-x"); !strings.Contains(got, "Alias client-x dihapus") {
		t.Errorf("remove: %q", got)
	}
	if out := e.cmd(t, "minggu"); !strings.Contains(out, "client-x 1j (VS Code)") || !strings.Contains(out, "Proyek X 30m") {
		t.Fatalf("after removing one alias:\n%s", out)
	}
	if got := e.cmd(t, `alias a/b x`); !strings.Contains(got, "Pakai: /proyek") {
		t.Errorf("path-like name: %q", got)
	}
}

func TestBlockStopsAndDeletes(t *testing.T) {
	e := newEnv(t)
	e.add(t, "secret-client", "code.exe", 40, e.now, 9)
	e.add(t, "open-source", "code.exe", 20, e.now, 11)
	if out := e.cmd(t, "blok Secret-Client"); !strings.Contains(out, "tidak akan dicatat lagi (40 catatan lama dihapus)") {
		t.Fatalf("block: %q", out)
	}
	if out := e.cmd(t, ""); strings.Contains(out, "secret-client") || !strings.Contains(out, "open-source") {
		t.Fatalf("after block:\n%s", out)
	}
	// the server drops it even if an agent that has not heard yet sends it
	n, err := e.db.UpsertProjectBuckets(context.Background(), e.dev, []store.ProjectBucket{
		{Start: e.now.Truncate(time.Hour), Project: "SECRET-client", Editor: "code.exe", ActiveSeconds: 60},
		{Start: e.now.Truncate(time.Hour), Project: "open-source", Editor: "code.exe", ActiveSeconds: 60},
	})
	if err != nil || n != 1 {
		t.Fatalf("stored %d (%v): a blocked project must not be stored", n, err)
	}
	if out := e.cmd(t, "blok"); !strings.Contains(out, "secret-client") {
		t.Errorf("blocklist: %q", out)
	}
	if bl, _ := e.db.ProjectBlocklist(context.Background()); len(bl) != 1 || bl[0] != "secret-client" {
		t.Errorf("blocklist %v (state sends it to the agent)", bl)
	}
	if out := e.cmd(t, "buka secret-client"); !strings.Contains(out, "boleh dicatat lagi") {
		t.Errorf("unblock: %q", out)
	}
	if bl, _ := e.db.ProjectBlocklist(context.Background()); len(bl) != 0 {
		t.Errorf("after unblock %v", bl)
	}
}

func TestForgetDeletesHistory(t *testing.T) {
	e := newEnv(t)
	e.add(t, "old-job", "code.exe", 25, e.now, 9)
	if out := e.cmd(t, "hapus OLD-JOB"); !strings.Contains(out, "Riwayat OLD-JOB dihapus (25 catatan)") {
		t.Fatalf("forget: %q", out)
	}
	if out := e.cmd(t, ""); strings.Contains(out, "old-job") {
		t.Fatalf("still there:\n%s", out)
	}
	// unlike a block, it can be recorded again
	e.add(t, "old-job", "code.exe", 5, e.now, 10)
	if out := e.cmd(t, ""); !strings.Contains(out, "old-job 5m") {
		t.Fatalf("recorded again:\n%s", out)
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 3; i++ { // a resend replaces the minute, it does not add to it
		e.add(t, "project-A", "code.exe", 10, e.now, 9)
	}
	if out := e.cmd(t, ""); !strings.Contains(out, "project-A 10m") {
		t.Fatalf("after three identical uploads:\n%s", out)
	}
}

func TestSplitArgs(t *testing.T) {
	for in, want := range map[string]string{
		`a b c`: "a|b|c", `  a   b `: "a|b", `alias "My Project" Other Name`: "alias|My Project|Other|Name", `"unterminated name`: "unterminated name", ``: "",
	} {
		if got := strings.Join(splitArgs(in), "|"); got != want {
			t.Errorf("splitArgs(%q) = %q, want %q", in, got, want)
		}
	}
}
