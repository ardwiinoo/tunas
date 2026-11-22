package store

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// PruneActivity deletes activity buckets that started before cutoff.
func (db *DB) PruneActivity(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from activity_buckets where bucket_start < $1`, cutoff)
	return tag.RowsAffected(), err
}

// PruneEvents deletes acknowledged events created before cutoff.
func (db *DB) PruneEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from events where acked_at is not null and created_at < $1`, cutoff)
	return tag.RowsAffected(), err
}

// Ping-style DB check with a short timeout (for /healthz).
func (db *DB) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	return db.Pool.QueryRow(ctx, `select 1`).Scan(&one)
}

// exportQueries are the owner's data, one JSON array per table. Device token
// hashes are deliberately excluded.
var exportQueries = []struct{ name, sql string }{
	{"tasks", `select coalesce(json_agg(t order by id), '[]') from tasks t`},
	{"reminders", `select coalesce(json_agg(r order by id), '[]') from (
		select id, task_id, text, remind_at, recurrence, status, attempts, sent_at, created_at from reminders) r`},
	{"devices", `select coalesce(json_agg(d order by id), '[]') from (
		select id, name, last_seen_at, revoked_at, created_at from devices) d`},
	{"settings", `select coalesce(json_object_agg(key, value), '{}') from settings`},
	{"llm_usage", `select coalesce(json_agg(u order by day), '[]') from llm_usage u`},
}

// Export writes a JSON document with all owner data to w. Activity is
// streamed row by row as per-minute buckets to keep memory flat.
func (db *DB) Export(ctx context.Context, w io.Writer, now time.Time) error {
	write := func(s string) error { _, err := io.WriteString(w, s); return err }
	hdr, _ := json.Marshal(now.UTC())
	if err := write(`{"exported_at":` + string(hdr)); err != nil {
		return err
	}
	for _, q := range exportQueries {
		var raw []byte
		if err := db.Pool.QueryRow(ctx, q.sql).Scan(&raw); err != nil {
			return err
		}
		if err := write(`,"` + q.name + `":` + string(raw)); err != nil {
			return err
		}
	}
	if err := write(`,"activity_buckets":[`); err != nil {
		return err
	}
	rows, err := db.Pool.Query(ctx, `select device_id, bucket_start, app, category, active_seconds, idle_seconds
		from activity_buckets order by bucket_start, app`)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.DeviceID, &b.Start, &b.App, &b.Category, &b.ActiveSeconds, &b.IdleSeconds); err != nil {
			return err
		}
		line, _ := json.Marshal(struct {
			DeviceID int64 `json:"device_id"`
			Bucket
		}{b.DeviceID, b})
		if !first {
			if err := write(","); err != nil {
				return err
			}
		}
		first = false
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return write("]}\n")
}
