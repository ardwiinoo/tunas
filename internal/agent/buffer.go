package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// Bucket is one minute of activity for one app (wire format to the server).
type Bucket struct {
	Start         time.Time `json:"bucket_start"`
	App           string    `json:"app"`
	Category      string    `json:"category"`
	ActiveSeconds int       `json:"active_seconds"`
	IdleSeconds   int       `json:"idle_seconds"`
}

// Buffer is the local SQLite store that survives offline periods.
type Buffer struct {
	db *sql.DB
}

const bufferSchema = `
create table if not exists buckets (
  bucket_start integer not null,      -- unix seconds, minute-aligned
  app text not null,
  category text not null,
  active integer not null default 0,
  idle integer not null default 0,
  dirty integer not null default 1,
  primary key (bucket_start, app)
);
create table if not exists kv (key text primary key, value text not null);
`

// OpenBuffer opens (or creates) the buffer database.
func OpenBuffer(path string) (*Buffer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(bufferSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init buffer: %w", err)
	}
	return &Buffer{db: db}, nil
}

// Close closes the database.
func (b *Buffer) Close() error { return b.db.Close() }

// Add accumulates seconds into the minute bucket. Totals are clamped to 60s per minute.
func (b *Buffer) Add(ctx context.Context, minute time.Time, app, category string, active, idle int) error {
	_, err := b.db.ExecContext(ctx, `
		insert into buckets(bucket_start, app, category, active, idle, dirty) values(?, ?, ?, min(60, ?), min(60, ?), 1)
		on conflict(bucket_start, app) do update set
		  active = min(60, active + excluded.active),
		  idle = max(0, min(60 - min(60, active + excluded.active), idle + excluded.idle)),
		  dirty = 1`,
		minute.Truncate(time.Minute).Unix(), app, category, active, idle)
	return err
}

// Pending returns up to limit unsynced buckets, oldest first.
func (b *Buffer) Pending(ctx context.Context, limit int) ([]Bucket, error) {
	rows, err := b.db.QueryContext(ctx,
		`select bucket_start, app, category, active, idle from buckets where dirty=1 order by bucket_start limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var bk Bucket
		var ts int64
		if err := rows.Scan(&ts, &bk.App, &bk.Category, &bk.ActiveSeconds, &bk.IdleSeconds); err != nil {
			return nil, err
		}
		bk.Start = time.Unix(ts, 0).UTC()
		out = append(out, bk)
	}
	return out, rows.Err()
}

// MarkSynced clears the dirty flag for buckets whose values did not change
// since they were read (a bucket updated meanwhile stays dirty and is resent).
func (b *Buffer) MarkSynced(ctx context.Context, bs []Bucket) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, bk := range bs {
		if _, err := tx.ExecContext(ctx,
			`update buckets set dirty=0 where bucket_start=? and app=? and active=? and idle=?`,
			bk.Start.Unix(), bk.App, bk.ActiveSeconds, bk.IdleSeconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Prune deletes synced buckets that started before cutoff (complete minutes).
func (b *Buffer) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := b.db.ExecContext(ctx, `delete from buckets where dirty=0 and bucket_start < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Count returns the number of buffered rows (all, and unsynced).
func (b *Buffer) Count(ctx context.Context) (total, dirty int, err error) {
	err = b.db.QueryRowContext(ctx, `select count(*), coalesce(sum(dirty), 0) from buckets`).Scan(&total, &dirty)
	return
}

// GetKV reads a value ("" if missing).
func (b *Buffer) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := b.db.QueryRowContext(ctx, `select value from kv where key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetKV writes a value.
func (b *Buffer) SetKV(ctx context.Context, key, value string) error {
	_, err := b.db.ExecContext(ctx,
		`insert into kv(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, key, value)
	return err
}
