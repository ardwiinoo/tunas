package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProjectBucket is one minute of editor time on one project.
type ProjectBucket struct {
	Start         time.Time `json:"bucket_start"`
	Project       string    `json:"project"`
	Editor        string    `json:"editor"`
	ActiveSeconds int       `json:"active_seconds"`
}

func projectKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// UpsertProjectBuckets stores buckets idempotently (a resent bucket replaces
// the stored one). Blocked projects are dropped here too, whatever the agent
// believes. It returns how many were stored.
func (db *DB) UpsertProjectBuckets(ctx context.Context, deviceID int64, bs []ProjectBucket) (int, error) {
	if len(bs) == 0 {
		return 0, nil
	}
	blocked, err := db.ProjectBlocklist(ctx)
	if err != nil {
		return 0, err
	}
	skip := map[string]bool{}
	for _, b := range blocked {
		skip[b] = true
	}
	n := 0
	err = pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, b := range bs {
			if skip[projectKey(b.Project)] {
				continue
			}
			batch.Queue(`insert into project_buckets(device_id, bucket_start, project, editor, active_seconds) values($1,$2,$3,$4,$5)
				on conflict (device_id, bucket_start, project, editor) do update set active_seconds = excluded.active_seconds`,
				deviceID, b.Start, b.Project, b.Editor, b.ActiveSeconds)
			n++
		}
		return tx.SendBatch(ctx, batch).Close()
	})
	return n, err
}

// ProjectTotal is the time on one project (after aliases), per editor.
type ProjectTotal struct {
	Name      string
	Seconds   int
	Editors   map[string]int // editor process → seconds
	Originals []string       // the recorded names behind Name (differs from Name when an alias applies)
}

// ProjectTotalsBetween totals editor time per project in [from, to), most
// time first. The current aliases apply (so an alias change reaches the past)
// and blocked projects are left out. Names that differ only in letter case or
// that share an alias are one project.
func (db *DB) ProjectTotalsBetween(ctx context.Context, from, to time.Time) ([]ProjectTotal, error) {
	rows, err := db.Pool.Query(ctx, `select coalesce(a.alias, b.project), b.project, b.editor, sum(b.active_seconds)::int
		from project_buckets b left join project_aliases a on a.project = lower(b.project)
		where b.bucket_start >= $1 and b.bucket_start < $2
		  and not exists (select 1 from project_blocklist bl where bl.project = lower(b.project))
		group by 1, 2, 3`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]*ProjectTotal{}
	variants := map[string]map[string]int{} // key → spelling → seconds (the display name is the most used spelling)
	for rows.Next() {
		var name, orig, editor string
		var secs int
		if err := rows.Scan(&name, &orig, &editor, &secs); err != nil {
			return nil, err
		}
		k := projectKey(name)
		t := byKey[k]
		if t == nil {
			t = &ProjectTotal{Name: name, Editors: map[string]int{}}
			byKey[k] = t
		}
		t.Seconds += secs
		t.Editors[editor] += secs
		if variants[k] == nil {
			variants[k] = map[string]int{}
		}
		variants[k][name] += secs
		if !contains(t.Originals, orig) {
			t.Originals = append(t.Originals, orig)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ProjectTotal, 0, len(byKey))
	for k, t := range byKey {
		best, bestSec := "", -1
		for n, sec := range variants[k] {
			if sec > bestSec || (sec == bestSec && n < best) {
				best, bestSec = n, sec
			}
		}
		t.Name = best
		sort.Strings(t.Originals)
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// SetProjectAlias shows a project under another name (empty alias removes it).
func (db *DB) SetProjectAlias(ctx context.Context, project, alias string) error {
	if alias == "" {
		_, err := db.Pool.Exec(ctx, `delete from project_aliases where project = $1`, projectKey(project))
		return err
	}
	_, err := db.Pool.Exec(ctx, `insert into project_aliases(project, alias) values($1, $2)
		on conflict (project) do update set alias = excluded.alias`, projectKey(project), alias)
	return err
}

// ProjectAliases returns original (lower-case) → alias.
func (db *DB) ProjectAliases(ctx context.Context) (map[string]string, error) {
	rows, err := db.Pool.Query(ctx, `select project, alias from project_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, a string
		if err := rows.Scan(&p, &a); err != nil {
			return nil, err
		}
		out[p] = a
	}
	return out, rows.Err()
}

// BlockProject stops a project from ever being recorded and deletes what was
// recorded for it. It returns the number of deleted rows.
func (db *DB) BlockProject(ctx context.Context, project string) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into project_blocklist(project) values($1) on conflict do nothing`, projectKey(project)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `delete from project_buckets where lower(project) = $1`, projectKey(project))
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// UnblockProject lets a project be recorded again.
func (db *DB) UnblockProject(ctx context.Context, project string) error {
	_, err := db.Pool.Exec(ctx, `delete from project_blocklist where project = $1`, projectKey(project))
	return err
}

// ProjectBlocklist returns the blocked projects (lower-case).
func (db *DB) ProjectBlocklist(ctx context.Context) ([]string, error) {
	rows, err := db.Pool.Query(ctx, `select project from project_blocklist order by project`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// DeleteProjectHistory deletes everything recorded for a project (the
// project can be recorded again afterwards).
func (db *DB) DeleteProjectHistory(ctx context.Context, project string) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from project_buckets where lower(project) = $1`, projectKey(project))
	return tag.RowsAffected(), err
}

// PruneProjectBuckets deletes project buckets older than cutoff.
func (db *DB) PruneProjectBuckets(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := db.Pool.Exec(ctx, `delete from project_buckets where bucket_start < $1`, cutoff)
	return tag.RowsAffected(), err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
