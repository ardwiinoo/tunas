package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Bucket is one minute of activity for one app.
type Bucket struct {
	DeviceID      int64     `json:"-"`
	Start         time.Time `json:"bucket_start"`
	App           string    `json:"app"`
	Category      string    `json:"category"`
	ActiveSeconds int       `json:"active_seconds"`
	IdleSeconds   int       `json:"idle_seconds"`
}

// UpsertBuckets stores buckets idempotently: a resent bucket replaces the
// stored totals (the agent always sends full per-minute totals).
func (db *DB) UpsertBuckets(ctx context.Context, deviceID int64, bs []Bucket) error {
	if len(bs) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, b := range bs {
			batch.Queue(`insert into activity_buckets(device_id, bucket_start, app, category, active_seconds, idle_seconds)
				values($1, $2, $3, $4, $5, $6)
				on conflict (device_id, bucket_start, app) do update
				set category=excluded.category, active_seconds=excluded.active_seconds, idle_seconds=excluded.idle_seconds`,
				deviceID, b.Start, b.App, b.Category, b.ActiveSeconds, b.IdleSeconds)
		}
		return tx.SendBatch(ctx, batch).Close()
	})
}

// BucketsBetween returns buckets in [from, to) for all devices, ordered by
// time. The category is the current server mapping (app_categories), so a
// change from Telegram applies to past days; an app without a mapping keeps
// the category the agent stored.
func (db *DB) BucketsBetween(ctx context.Context, from, to time.Time) ([]Bucket, error) {
	rows, err := db.Pool.Query(ctx,
		`select b.device_id, b.bucket_start, b.app, coalesce(ac.category, b.category), b.active_seconds, b.idle_seconds
		 from activity_buckets b left join app_categories ac on ac.app = lower(b.app)
		 where b.bucket_start >= $1 and b.bucket_start < $2
		 order by b.bucket_start, b.app`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Bucket, error) {
		var b Bucket
		return b, r.Scan(&b.DeviceID, &b.Start, &b.App, &b.Category, &b.ActiveSeconds, &b.IdleSeconds)
	})
}

// GetSetting reads a JSON setting into dst. Returns false if missing.
func (db *DB) GetSetting(ctx context.Context, key string, dst any) (bool, error) {
	var raw []byte
	err := db.Pool.QueryRow(ctx, `select value from settings where key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, dst)
}

// SetSetting upserts a JSON setting.
func (db *DB) SetSetting(ctx context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx,
		`insert into settings(key, value) values($1, $2::jsonb) on conflict (key) do update set value=excluded.value`,
		key, string(raw))
	return err
}

// DeleteSetting removes a setting.
func (db *DB) DeleteSetting(ctx context.Context, key string) error {
	_, err := db.Pool.Exec(ctx, `delete from settings where key=$1`, key)
	return err
}

// IncrementLLMUsage bumps the request counter for the local day of t and returns the new count.
func (db *DB) IncrementLLMUsage(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx,
		`insert into llm_usage(day, requests) values($1::date, 1)
		 on conflict (day) do update set requests = llm_usage.requests + 1 returning requests`,
		t.Format("2006-01-02")).Scan(&n)
	return n, err
}

// LLMUsageOn returns the request count of the local day of t (0 if none).
func (db *DB) LLMUsageOn(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx, `select coalesce(max(requests), 0) from llm_usage where day = $1::date`, t.Format("2006-01-02")).Scan(&n)
	return n, err
}
