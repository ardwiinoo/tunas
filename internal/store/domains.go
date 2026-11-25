package store

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DomainBucket is one minute on one website (hostname only).
type DomainBucket struct {
	Start         time.Time `json:"bucket_start"`
	Domain        string    `json:"domain"`
	ActiveSeconds int       `json:"active_seconds"`
}

// UpsertDomainBuckets stores buckets idempotently. Blocked domains (and
// subdomains of them) are dropped here whatever the agent believes.
func (db *DB) UpsertDomainBuckets(ctx context.Context, deviceID int64, bs []DomainBucket) (int, error) {
	if len(bs) == 0 {
		return 0, nil
	}
	blocked, err := db.DomainBlocklist(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	err = pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, b := range bs {
			if domainBlocked(b.Domain, blocked) {
				continue
			}
			batch.Queue(`insert into domain_buckets(device_id, bucket_start, domain, active_seconds) values($1,$2,$3,$4)
				on conflict (device_id, bucket_start, domain) do update set active_seconds = excluded.active_seconds`,
				deviceID, b.Start, b.Domain, b.ActiveSeconds)
			n++
		}
		return tx.SendBatch(ctx, batch).Close()
	})
	return n, err
}

// domainBlocked reports whether host equals or is a subdomain of a blocked domain.
func domainBlocked(host string, blocked []string) bool {
	for _, b := range blocked {
		if host == b || strings.HasSuffix(host, "."+b) {
			return true
		}
	}
	return false
}

// DomainTotal is the time on one website.
type DomainTotal struct {
	Domain   string
	Seconds  int
	Category string // from domain_categories (by suffix); "" = none yet
}

// DomainTotalsBetween totals time per domain in [from, to), most first.
// Blocked domains are left out; categories are matched by suffix.
func (db *DB) DomainTotalsBetween(ctx context.Context, from, to time.Time) ([]DomainTotal, error) {
	rows, err := db.Pool.Query(ctx, `select domain, sum(active_seconds)::int from domain_buckets
		where bucket_start >= $1 and bucket_start < $2 group by domain order by 2 desc, domain`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainTotal
	for rows.Next() {
		var t DomainTotal
		if err := rows.Scan(&t.Domain, &t.Seconds); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	blocked, err := db.DomainBlocklist(ctx)
	if err != nil {
		return nil, err
	}
	cats, err := db.DomainCategories(ctx)
	if err != nil {
		return nil, err
	}
	kept := out[:0]
	for _, t := range out {
		if domainBlocked(t.Domain, blocked) {
			continue
		}
		t.Category = matchDomain(t.Domain, cats)
		kept = append(kept, t)
	}
	return kept, nil
}

// matchDomain returns the category of the host or its nearest parent ("" if none).
func matchDomain(host string, cats map[string]string) string {
	for h := host; ; {
		if c, ok := cats[h]; ok {
			return c
		}
		i := strings.IndexByte(h, '.')
		if i < 0 || strings.Count(h, ".") < 2 {
			return ""
		}
		h = h[i+1:]
	}
}

// DomainCategories returns the mapping (lower-case domain → category).
func (db *DB) DomainCategories(ctx context.Context) (map[string]string, error) {
	rows, err := db.Pool.Query(ctx, `select domain, category from domain_categories`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d, c string
		if err := rows.Scan(&d, &c); err != nil {
			return nil, err
		}
		out[d] = c
	}
	return out, rows.Err()
}

// SetDomainCategory maps a domain to a category.
func (db *DB) SetDomainCategory(ctx context.Context, domain, category string, now time.Time) error {
	_, err := db.Pool.Exec(ctx, `insert into domain_categories(domain, category, updated_at) values($1,$2,$3)
		on conflict (domain) do update set category = excluded.category, updated_at = excluded.updated_at`, domain, category, now)
	return err
}

// SeedDomainCategories adds domains that have no category yet.
func (db *DB) SeedDomainCategories(ctx context.Context, m map[string]string, now time.Time) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		for d, c := range m {
			tag, err := tx.Exec(ctx, `insert into domain_categories(domain, category, updated_at) values($1,$2,$3) on conflict do nothing`, d, c, now)
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return nil
	})
	return n, err
}

// UncategorizedDomains lists domains with no category (by suffix) whose total
// time is at least min seconds.
func (db *DB) UncategorizedDomains(ctx context.Context, min int) ([]DomainTotal, error) {
	rows, err := db.Pool.Query(ctx, `select domain, sum(active_seconds)::int from domain_buckets group by domain
		having sum(active_seconds) >= $1 order by 2 desc`, min)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []DomainTotal
	for rows.Next() {
		var t DomainTotal
		if err := rows.Scan(&t.Domain, &t.Seconds); err != nil {
			return nil, err
		}
		all = append(all, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cats, err := db.DomainCategories(ctx)
	if err != nil {
		return nil, err
	}
	var out []DomainTotal
	for _, t := range all {
		if matchDomain(t.Domain, cats) == "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// BlockDomain stops a domain (and its subdomains) from ever being recorded
// and deletes what was recorded. It returns the number of deleted rows.
func (db *DB) BlockDomain(ctx context.Context, domain string) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into domain_blocklist(domain) values($1) on conflict do nothing`, domain); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from domain_buckets where domain = $1 or domain like '%.' || $1`, domain)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// UnblockDomain lets a domain be recorded again.
func (db *DB) UnblockDomain(ctx context.Context, domain string) error {
	_, err := db.Pool.Exec(ctx, `delete from domain_blocklist where domain = $1`, domain)
	return err
}

// DomainBlocklist returns the blocked domains.
func (db *DB) DomainBlocklist(ctx context.Context) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `select domain from domain_blocklist order by domain`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// DeleteDomainHistory deletes what was recorded for a domain and its subdomains.
func (db *DB) DeleteDomainHistory(ctx context.Context, domain string) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from domain_buckets where domain = $1 or domain like '%.' || $1`, domain)
	return tag.RowsAffected(), err
}

// PruneDomainBuckets deletes domain buckets older than cutoff.
func (db *DB) PruneDomainBuckets(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from domain_buckets where bucket_start < $1`, cutoff)
	return tag.RowsAffected(), err
}
