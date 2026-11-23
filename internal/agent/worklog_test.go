package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/api"
	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
	"github.com/ardwiinoo/tunas/internal/worklog"
)

// switchAPI is the real client, or "no network" while offline is set.
type switchAPI struct {
	*Client
	offline atomic.Bool
}

var errNoNetwork = &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("network is unreachable")}

func (s *switchAPI) PostWorklog(ctx context.Context, it WorklogItem) (WorklogSaved, error) {
	if s.offline.Load() {
		return WorklogSaved{}, errNoNetwork
	}
	return s.Client.PostWorklog(ctx, it)
}

// worklogAPI adapts the real service for the test server (as cmd/server does).
type worklogAPI struct{ svc *worklog.Service }

func (w worklogAPI) Save(ctx context.Context, date, text, clientID string, promptID int64) (api.WorklogSaved, error) {
	r, err := w.svc.SaveFromAgent(ctx, date, text, clientID, promptID)
	var bad *worklog.InputError
	if errors.As(err, &bad) {
		return api.WorklogSaved{}, &api.WorklogInputError{Reason: bad.Reason}
	}
	return api.WorklogSaved{ID: r.ID, Count: r.Count, Duplicate: r.Duplicate}, err
}
func (w worklogAPI) Today(context.Context) (api.WorklogToday, error) { return api.WorklogToday{}, nil }
func (w worklogAPI) Answer(context.Context, int64, string) error     { return nil }

func worklogRig(t *testing.T) (*Agent, *switchAPI, *store.DB) {
	db := storetest.New(t)
	ctx := context.Background()
	_, token, _ := db.CreateDevice(ctx, "laptop")
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	svc := &worklog.Service{DB: db, Loc: jkt, Now: time.Now, Log: slog.New(slog.DiscardHandler), ChatID: 1}
	srv := httptest.NewServer(api.New(slog.New(slog.DiscardHandler), api.Options{DB: db, Worklog: worklogAPI{svc}}))
	t.Cleanup(srv.Close)
	sw := &switchAPI{Client: NewClient(srv.URL, token)}
	a := &Agent{Cfg: testCfg(), Buf: openBuf(t), API: sw, Log: slog.New(slog.DiscardHandler)}
	a.init()
	return a, sw, db
}

func rowsToday(t *testing.T, db *store.DB) []store.WorklogEntry {
	t.Helper()
	jkt, _ := time.LoadLocation("Asia/Jakarta")
	es, err := db.WorklogByDate(context.Background(), worklog.LocalDate(time.Now(), jkt))
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// Offline write → kept locally; connection back → sent; resent twice →
// still exactly one row.
func TestWorklogOfflineThenResendTwice(t *testing.T) {
	a, sw, db := worklogRig(t)
	ctx := context.Background()
	var queued []int
	a.OnWorklogQueue = func(n int) { queued = append(queued, n) }

	sw.offline.Store(true)
	res, err := a.WriteWorklog(ctx, "nulis saat offline", 0)
	if err != nil || !res.Queued || res.Synced {
		t.Fatalf("offline write = %+v %v (want queued)", res, err)
	}
	if a.QueuedWorklog(ctx) != 1 || len(rowsToday(t, db)) != 0 {
		t.Fatal("kept locally only")
	}
	if n, err := a.FlushWorklog(ctx); n != 0 || err == nil {
		t.Fatal("still offline: nothing sent, entry kept")
	}
	items, _ := a.Buf.PendingWorklog(ctx)
	if len(items) != 1 || len(items[0].ClientID) != 36 {
		t.Fatalf("queue %+v", items)
	}

	sw.offline.Store(false)
	if n, err := a.FlushWorklog(ctx); n != 1 || err != nil {
		t.Fatalf("back online: sent %d, %v", n, err)
	}
	if a.QueuedWorklog(ctx) != 0 {
		t.Fatal("removed only after the server confirmed")
	}
	// the same entry arrives twice more (e.g. the confirmation got lost)
	for i := range 2 {
		saved, err := sw.Client.PostWorklog(ctx, items[0])
		if err != nil || !saved.Duplicate {
			t.Fatalf("resend %d: %+v %v", i+1, saved, err)
		}
	}
	rows := rowsToday(t, db)
	if len(rows) != 1 || rows[0].Text != "nulis saat offline" || rows[0].Source != "agent" {
		t.Fatalf("rows = %+v, want exactly one", rows)
	}
	if got := queued[len(queued)-1]; got != 0 || queued[0] != 1 {
		t.Fatalf("tray counts %v", queued)
	}
}

func TestWorklogOnlineAndRejected(t *testing.T) {
	a, _, db := worklogRig(t)
	ctx := context.Background()
	res, err := a.WriteWorklog(ctx, "langsung tersimpan", 0)
	if err != nil || !res.Synced || res.Count != 1 || a.QueuedWorklog(ctx) != 0 {
		t.Fatalf("online write = %+v %v", res, err)
	}
	// rejected for good (too long): not kept, the reason is shown
	res, err = a.WriteWorklog(ctx, strings.Repeat("x", 2001), 0)
	if err != nil || res.Reject == "" || res.Synced || a.QueuedWorklog(ctx) != 0 {
		t.Fatalf("rejected write = %+v %v", res, err)
	}
	if len(rowsToday(t, db)) != 1 {
		t.Fatal("only the valid entry is stored")
	}
}

// The resend loop sends the queue as soon as the connection is back.
func TestWorklogLoopResendsOnReconnect(t *testing.T) {
	a, sw, db := worklogRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sw.offline.Store(true)
	if _, err := a.WriteWorklog(ctx, "antre", 0); err != nil {
		t.Fatal(err)
	}
	go a.worklogLoop(ctx)
	sw.offline.Store(false)
	a.kickWorklog() // what the event loop does when it reconnects
	deadline := time.Now().Add(5 * time.Second)
	for a.QueuedWorklog(ctx) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if a.QueuedWorklog(ctx) != 0 || len(rowsToday(t, db)) != 1 {
		t.Fatal("queue not sent after reconnect")
	}
}
