// Package store is the Postgres data layer (pgx). All queries are parameterized.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ardwiinoo/tunas/migrations"
)

// ErrNotFound is returned when a row does not exist (or is not in a usable state).
var ErrNotFound = errors.New("not found")

// DB wraps a pgx pool.
type DB struct {
	Pool   *pgxpool.Pool
	events broadcaster
}

// Open connects to Postgres. maxConns keeps memory small on the VPS.
func Open(ctx context.Context, url string, maxConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx parse errors may echo the URL; do not wrap.
		return nil, errors.New("invalid DATABASE_URL")
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect db: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close closes the pool.
func (db *DB) Close() { db.Pool.Close() }

// Ping checks connectivity.
func (db *DB) Ping(ctx context.Context) error { return db.Pool.Ping(ctx) }

// migrationLockID is an arbitrary constant for pg_advisory_xact_lock.
const migrationLockID = 727_001

// Migrate applies pending migrations in order. Each migration runs in its own
// transaction; an advisory lock prevents concurrent runners.
func (db *DB) Migrate(ctx context.Context) ([]string, error) {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	if _, err := db.Pool.Exec(ctx, `create table if not exists schema_migrations (
		version text primary key, applied_at timestamptz not null default now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	var applied []string
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return applied, err
		}
		done, err := db.applyOne(ctx, version, string(body))
		if err != nil {
			return applied, fmt.Errorf("migration %s: %w", version, err)
		}
		if done {
			applied = append(applied, version)
		}
	}
	return applied, nil
}

func (db *DB) applyOne(ctx context.Context, version, body string) (bool, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return false, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from schema_migrations where version=$1)`, version).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if _, err := tx.Exec(ctx, body); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `insert into schema_migrations(version) values($1)`, version); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
