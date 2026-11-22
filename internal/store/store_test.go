package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
	"github.com/ardwiinoo/tunas/internal/store/storetest"
)

func TestMigrateIdempotent(t *testing.T) {
	db := storetest.New(t)
	applied, err := db.Migrate(context.Background())
	if err != nil || len(applied) != 0 {
		t.Fatalf("second migrate: %v %v", applied, err)
	}
}

func TestTasksAndEvents(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := time.Now()

	task, err := db.CreateTask(ctx, "beli kopi")
	if err != nil {
		t.Fatal(err)
	}
	open, _ := db.OpenTasks(ctx, 10)
	if len(open) != 1 || open[0].Title != "beli kopi" {
		t.Fatalf("open = %+v", open)
	}
	if _, err := db.CompleteTask(ctx, task.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteTask(ctx, task.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("double complete err = %v", err)
	}
	if _, err := db.CompleteTask(ctx, 9999, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing complete err = %v", err)
	}

	evs, err := db.EventsAfter(ctx, 1, 0, 10)
	if err != nil || len(evs) != 2 || evs[0].Type != "task.created" || evs[1].Type != "task.done" {
		t.Fatalf("events = %+v %v", evs, err)
	}
	if n, _ := db.AckEvents(ctx, 1, evs[0].ID, now); n != 1 {
		t.Fatalf("acked %d", n)
	}
	evs, _ = db.EventsAfter(ctx, 1, 0, 10)
	if len(evs) != 1 {
		t.Fatalf("after ack = %+v", evs)
	}
}

func TestPollEventsWakesUp(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	done := make(chan []store.Event, 1)
	go func() {
		evs, _ := db.PollEvents(ctx, 1, 0, 10, 5*time.Second)
		done <- evs
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := db.CreateEvent(ctx, "form.set", map[string]any{"form": "fokus"}); err != nil {
		t.Fatal(err)
	}
	evs := <-done
	if len(evs) != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("poll returned %+v after %v", evs, time.Since(start))
	}
	// timeout path
	evs, err := db.PollEvents(ctx, 1, evs[0].ID, 10, 100*time.Millisecond)
	if err != nil || len(evs) != 0 {
		t.Fatalf("timeout poll = %+v %v", evs, err)
	}
}

func TestDevices(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	now := time.Now()

	d, token, err := db.CreateDevice(ctx, "laptop")
	if err != nil || token == "" {
		t.Fatal(err)
	}
	if _, _, err := db.CreateDevice(ctx, "laptop"); !errors.Is(err, store.ErrExists) {
		t.Fatalf("dup err = %v", err)
	}
	var stored string
	_ = db.Pool.QueryRow(ctx, `select token_hash from devices where id=$1`, d.ID).Scan(&stored)
	if stored == token || stored != store.HashToken(token) {
		t.Fatal("token must be stored hashed")
	}
	got, err := db.AuthDevice(ctx, token, now)
	if err != nil || got.ID != d.ID || got.LastSeenAt == nil {
		t.Fatalf("auth = %+v %v", got, err)
	}
	if _, err := db.AuthDevice(ctx, "wrong", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("wrong token accepted")
	}
	if _, err := db.LastSeenDevice(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeDevice(ctx, "laptop", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthDevice(ctx, token, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("revoked token accepted")
	}
	if err := db.RevokeDevice(ctx, "laptop", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("double revoke")
	}
}
