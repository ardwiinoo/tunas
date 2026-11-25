package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppCategories returns the mapping (lower-case app → category).
func (db *DB) AppCategories(ctx context.Context) (map[string]string, error) {
	rows, err := db.Pool.Query(ctx, `select app, category from app_categories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var app, cat string
		if err := rows.Scan(&app, &cat); err != nil {
			return nil, err
		}
		out[app] = cat
	}
	return out, rows.Err()
}

// SetAppCategory maps an app (case-insensitive) to a category.
func (db *DB) SetAppCategory(ctx context.Context, app, category string, now time.Time) error {
	_, err := db.Pool.Exec(ctx, `insert into app_categories(app, category, updated_at) values($1, $2, $3)
		on conflict (app) do update set category = excluded.category, updated_at = excluded.updated_at`,
		strings.ToLower(app), category, now)
	return err
}

// SeedAppCategories adds the apps that have no category yet and returns how
// many were new. Existing choices (made from Telegram) are never overwritten.
func (db *DB) SeedAppCategories(ctx context.Context, apps map[string]string, now time.Time) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		for app, cat := range apps {
			tag, err := tx.Exec(ctx, `insert into app_categories(app, category, updated_at) values($1, $2, $3)
				on conflict (app) do nothing`, strings.ToLower(app), cat, now)
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return nil
	})
	return n, err
}

// CategoriesETag identifies a mapping: the agent sends it back and gets the
// full mapping only when it changed.
func CategoriesETag(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\x00" + m[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// AppUsage is one app's total active time in a range.
type AppUsage struct {
	App      string // a name as the agent reports it
	Seconds  int
	Category string // current mapping; "" = not categorized yet
}

// AppUsageBetween totals active seconds per app (case-insensitive) in
// [from, to), most used first, with the current category of each.
func (db *DB) AppUsageBetween(ctx context.Context, from, to time.Time) ([]AppUsage, error) {
	rows, err := db.Pool.Query(ctx, `select max(b.app), sum(b.active_seconds)::int, coalesce(max(ac.category), '')
		from activity_buckets b left join app_categories ac on ac.app = lower(b.app)
		where b.bucket_start >= $1 and b.bucket_start < $2
		group by lower(b.app) having sum(b.active_seconds) > 0
		order by sum(b.active_seconds) desc, lower(b.app)`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AppUsage, error) {
		var u AppUsage
		return u, r.Scan(&u.App, &u.Seconds, &u.Category)
	})
}

// UncategorizedApps lists apps with no category whose total active time
// (all history) is at least min seconds.
func (db *DB) UncategorizedApps(ctx context.Context, min int) ([]AppUsage, error) {
	rows, err := db.Pool.Query(ctx, `select max(b.app), sum(b.active_seconds)::int, ''
		from activity_buckets b left join app_categories ac on ac.app = lower(b.app)
		where ac.app is null
		group by lower(b.app) having sum(b.active_seconds) >= $1
		order by sum(b.active_seconds) desc`, min)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (AppUsage, error) {
		var u AppUsage
		return u, r.Scan(&u.App, &u.Seconds, &u.Category)
	})
}
