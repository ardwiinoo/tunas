package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Event is a server→agent notification.
type Event struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// broadcaster wakes long-poll waiters when events are committed. The server is
// a single process, so an in-memory signal is enough (no LISTEN/NOTIFY).
type broadcaster struct {
	mu sync.Mutex
	ch chan struct{}
}

func (b *broadcaster) wait() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch == nil {
		b.ch = make(chan struct{})
	}
	return b.ch
}

func (b *broadcaster) signal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ch != nil {
		close(b.ch)
		b.ch = nil
	}
}

func insertEvent(ctx context.Context, tx pgx.Tx, typ string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into events(type, payload) values($1, $2)`, typ, b)
	return err
}

// withEvents runs fn in a transaction and wakes event waiters after commit.
func (db *DB) withEvents(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := pgx.BeginFunc(ctx, db.Pool, fn); err != nil {
		return err
	}
	db.events.signal()
	return nil
}

// CreateEvent inserts a broadcast event (device_id null).
func (db *DB) CreateEvent(ctx context.Context, typ string, payload any) error {
	return db.withEvents(ctx, func(tx pgx.Tx) error { return insertEvent(ctx, tx, typ, payload) })
}

// EventsAfter returns unacked events for the device (or broadcast) with id > after.
func (db *DB) EventsAfter(ctx context.Context, deviceID, after int64, limit int) ([]Event, error) {
	rows, err := db.Pool.Query(ctx,
		`select id, type, payload, created_at from events
		 where id > $2 and (device_id is null or device_id = $1) and acked_at is null
		 order by id limit $3`, deviceID, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		return e, r.Scan(&e.ID, &e.Type, &e.Payload, &e.CreatedAt)
	})
}

// PollEvents returns events with id > after, waiting up to wait for new ones.
func (db *DB) PollEvents(ctx context.Context, deviceID, after int64, limit int, wait time.Duration) ([]Event, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		woke := db.events.wait() // subscribe before querying to avoid a missed wakeup
		evs, err := db.EventsAfter(ctx, deviceID, after, limit)
		if err != nil || len(evs) > 0 {
			return evs, err
		}
		select {
		case <-woke:
		case <-deadline.C:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// AckEvents marks events up to upTo as acked for this device.
// Broadcast events are acked when any device acks them (single-owner product).
func (db *DB) AckEvents(ctx context.Context, deviceID, upTo int64, now time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx,
		`update events set acked_at=$3 where id <= $2 and acked_at is null
		 and (device_id is null or device_id = $1)`, deviceID, upTo, now)
	return tag.RowsAffected(), err
}
