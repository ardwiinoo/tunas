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
create table if not exists project_buckets (  -- project names only, never window titles
  bucket_start integer not null,
  project text not null,
  editor text not null,                -- lower-case process name
  active integer not null default 0,
  dirty integer not null default 1,
  primary key (bucket_start, project, editor)
);
create table if not exists domain_buckets (  -- hostnames only
  bucket_start integer not null,
  domain text not null,
  active integer not null default 0,
  dirty integer not null default 1,
  primary key (bucket_start, domain)
);
create table if not exists kv (key text primary key, value text not null);
create table if not exists worklog_queue (   -- worklog entries written on the laptop, until the server confirms
  client_id text primary key,                -- UUID: resending is harmless (server is idempotent)
  entry_date text not null,                  -- YYYY-MM-DD, the local day it was written
  text text not null,
  prompt_id integer not null default 0,
  created_at integer not null                -- unix seconds
);
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

// WorklogItem is a worklog entry waiting to reach the server.
type WorklogItem struct {
	ClientID  string    `json:"client_id"`
	Date      string    `json:"date"`
	Text      string    `json:"text"`
	PromptID  int64     `json:"prompt_id,omitempty"`
	CreatedAt time.Time `json:"-"` // local only
}

// QueueWorklog stores an entry before it is sent.
func (b *Buffer) QueueWorklog(ctx context.Context, it WorklogItem) error {
	_, err := b.db.ExecContext(ctx, `insert or ignore into worklog_queue(client_id, entry_date, text, prompt_id, created_at)
		values(?, ?, ?, ?, ?)`, it.ClientID, it.Date, it.Text, it.PromptID, it.CreatedAt.Unix())
	return err
}

// PendingWorklog lists queued entries, oldest first.
func (b *Buffer) PendingWorklog(ctx context.Context) ([]WorklogItem, error) {
	rows, err := b.db.QueryContext(ctx, `select client_id, entry_date, text, prompt_id, created_at from worklog_queue order by created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorklogItem
	for rows.Next() {
		var it WorklogItem
		var ts int64
		if err := rows.Scan(&it.ClientID, &it.Date, &it.Text, &it.PromptID, &ts); err != nil {
			return nil, err
		}
		it.CreatedAt = time.Unix(ts, 0)
		out = append(out, it)
	}
	return out, rows.Err()
}

// DoneWorklog removes an entry the server confirmed (or rejected for good).
func (b *Buffer) DoneWorklog(ctx context.Context, clientID string) error {
	_, err := b.db.ExecContext(ctx, `delete from worklog_queue where client_id = ?`, clientID)
	return err
}

// CountPending is the number of activity buckets waiting to sync.
func (b *Buffer) CountPending(ctx context.Context) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx, `select count(*) from buckets where dirty=1`).Scan(&n)
	return n, err
}

// AddProject accumulates seconds into the minute bucket of a project.
func (b *Buffer) AddProject(ctx context.Context, minute time.Time, project, editor string, active int) error {
	_, err := b.db.ExecContext(ctx, `
		insert into project_buckets(bucket_start, project, editor, active, dirty) values(?, ?, ?, min(60, ?), 1)
		on conflict(bucket_start, project, editor) do update set active = min(60, active + excluded.active), dirty = 1`,
		minute.Truncate(time.Minute).Unix(), project, editor, active)
	return err
}

// PendingProjects returns up to limit unsynced project buckets, oldest first.
func (b *Buffer) PendingProjects(ctx context.Context, limit int) ([]ProjectBucket, error) {
	rows, err := b.db.QueryContext(ctx,
		`select bucket_start, project, editor, active from project_buckets where dirty=1 order by bucket_start limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectBucket
	for rows.Next() {
		var pb ProjectBucket
		var ts int64
		if err := rows.Scan(&ts, &pb.Project, &pb.Editor, &pb.ActiveSeconds); err != nil {
			return nil, err
		}
		pb.Start = time.Unix(ts, 0).UTC()
		out = append(out, pb)
	}
	return out, rows.Err()
}

// MarkProjectsSynced clears the dirty flag of buckets that did not change
// since they were read.
func (b *Buffer) MarkProjectsSynced(ctx context.Context, bs []ProjectBucket) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, pb := range bs {
		if _, err := tx.ExecContext(ctx,
			`update project_buckets set dirty=0 where bucket_start=? and project=? and editor=? and active=?`,
			pb.Start.Unix(), pb.Project, pb.Editor, pb.ActiveSeconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneProjects deletes synced project buckets older than cutoff.
func (b *Buffer) PruneProjects(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := b.db.ExecContext(ctx, `delete from project_buckets where dirty=0 and bucket_start < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeProjects forgets every buffered project bucket (recording turned off
// does not need it, but the owner may want the local copy gone).
func (b *Buffer) PurgeProjects(ctx context.Context) error {
	_, err := b.db.ExecContext(ctx, `delete from project_buckets`)
	return err
}

// AddDomain accumulates seconds into the minute bucket of a website.
func (b *Buffer) AddDomain(ctx context.Context, minute time.Time, domain string, active int) error {
	_, err := b.db.ExecContext(ctx, `
		insert into domain_buckets(bucket_start, domain, active, dirty) values(?, ?, min(60, ?), 1)
		on conflict(bucket_start, domain) do update set active = min(60, active + excluded.active), dirty = 1`,
		minute.Truncate(time.Minute).Unix(), domain, active)
	return err
}

// DomainBucket is one minute on one website (wire format).
type DomainBucket struct {
	Start         time.Time `json:"bucket_start"`
	Domain        string    `json:"domain"`
	ActiveSeconds int       `json:"active_seconds"`
}

// PendingDomains returns up to limit unsynced domain buckets, oldest first.
func (b *Buffer) PendingDomains(ctx context.Context, limit int) ([]DomainBucket, error) {
	rows, err := b.db.QueryContext(ctx,
		`select bucket_start, domain, active from domain_buckets where dirty=1 order by bucket_start limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainBucket
	for rows.Next() {
		var d DomainBucket
		var ts int64
		if err := rows.Scan(&ts, &d.Domain, &d.ActiveSeconds); err != nil {
			return nil, err
		}
		d.Start = time.Unix(ts, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDomainsSynced clears the dirty flag of buckets that did not change since they were read.
func (b *Buffer) MarkDomainsSynced(ctx context.Context, bs []DomainBucket) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, d := range bs {
		if _, err := tx.ExecContext(ctx, `update domain_buckets set dirty=0 where bucket_start=? and domain=? and active=?`,
			d.Start.Unix(), d.Domain, d.ActiveSeconds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneDomains deletes synced domain buckets older than cutoff.
func (b *Buffer) PruneDomains(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := b.db.ExecContext(ctx, `delete from domain_buckets where dirty=0 and bucket_start < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
