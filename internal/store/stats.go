package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// StatsRetention is how long stats_samples are kept.
const StatsRetention = 7 * 24 * time.Hour

// SetDeviceStats stores the agent's latest statistics (devices.last_stats).
func (db *DB) SetDeviceStats(ctx context.Context, deviceID int64, stats json.RawMessage) error {
	_, err := db.Pool.Exec(ctx, `update devices set last_stats = $2 where id = $1`, deviceID, []byte(stats))
	return err
}

// DeviceStats returns the latest stats of a device (nil when none yet).
func (db *DB) DeviceStats(ctx context.Context, deviceID int64) (json.RawMessage, error) {
	var raw []byte
	err := db.Pool.QueryRow(ctx, `select last_stats from devices where id = $1`, deviceID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return raw, err
}

// AddStatsSample stores a sample (scope server|agent|host). A second sample
// with the same ts, scope and device is ignored.
func (db *DB) AddStatsSample(ctx context.Context, ts time.Time, scope string, deviceID int64, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var dev any
	if deviceID != 0 {
		dev = deviceID
	}
	_, err = db.Pool.Exec(ctx, `insert into stats_samples(ts, scope, device_id, data) values ($1, $2, $3, $4)
		on conflict do nothing`, ts, scope, dev, raw)
	return err
}

// LastStatsSampleAt is the newest sample time for a scope and device (zero if none).
func (db *DB) LastStatsSampleAt(ctx context.Context, scope string, deviceID int64) (time.Time, error) {
	var t *time.Time
	err := db.Pool.QueryRow(ctx, `select max(ts) from stats_samples where scope = $1 and coalesce(device_id, 0) = $2`, scope, deviceID).Scan(&t)
	if t == nil {
		return time.Time{}, err
	}
	return *t, err
}

// StatsSince returns the samples of a scope newer than since, oldest first.
func (db *DB) StatsSince(ctx context.Context, scope string, deviceID int64, since time.Time) ([]StatsSample, error) {
	rows, err := db.Pool.Query(ctx, `select ts, data from stats_samples
		where scope = $1 and coalesce(device_id, 0) = $2 and ts >= $3 order by ts`, scope, deviceID, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatsSample, error) {
		var s StatsSample
		var raw []byte
		if err := r.Scan(&s.TS, &raw); err != nil {
			return s, err
		}
		s.Data = raw
		return s, nil
	})
}

// StatsSample is one stored sample.
type StatsSample struct {
	TS   time.Time
	Data json.RawMessage
}

// PruneStats deletes samples older than cutoff.
func (db *DB) PruneStats(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from stats_samples where ts < $1`, cutoff)
	return tag.RowsAffected(), err
}

// DBSize is the size of the current database in bytes.
func (db *DB) DBSize(ctx context.Context) (int64, error) {
	var n int64
	err := db.Pool.QueryRow(ctx, `select pg_database_size(current_database())`).Scan(&n)
	return n, err
}

// TableSize is one table and its total size (data, indexes, toast).
type TableSize struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// BiggestTables lists the n largest tables of the current schema.
func (db *DB) BiggestTables(ctx context.Context, n int) ([]TableSize, error) {
	rows, err := db.Pool.Query(ctx, `select c.relname, pg_total_relation_size(c.oid)
		from pg_class c join pg_namespace ns on ns.oid = c.relnamespace
		where c.relkind = 'r' and ns.nspname = current_schema()
		order by 2 desc limit $1`, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TableSize, error) {
		var t TableSize
		return t, r.Scan(&t.Name, &t.Bytes)
	})
}

// PoolStats is the connection pool state.
func (db *DB) PoolStats() (acquired, total, max int32) {
	st := db.Pool.Stat()
	return st.AcquiredConns(), st.TotalConns(), st.MaxConns()
}
