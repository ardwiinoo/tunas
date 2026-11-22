package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrExists is returned when a unique name is taken.
var ErrExists = errors.New("already exists")

// Device is a registered agent.
type Device struct {
	ID         int64
	Name       string
	LastSeenAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// HashToken returns the hex sha256 of a raw device token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateDevice registers a device and returns its raw token (shown once; only the hash is stored).
func (db *DB) CreateDevice(ctx context.Context, name string) (Device, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Device{}, "", err
	}
	token := "tnd_" + base64.RawURLEncoding.EncodeToString(buf)
	var d Device
	err := db.Pool.QueryRow(ctx,
		`insert into devices(name, token_hash) values($1, $2) returning id, name, created_at`,
		name, HashToken(token)).Scan(&d.ID, &d.Name, &d.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Device{}, "", ErrExists
	}
	return d, token, err
}

// ListDevices returns all devices.
func (db *DB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := db.Pool.Query(ctx,
		`select id, name, last_seen_at, revoked_at, created_at from devices order by id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Device, error) {
		var d Device
		return d, r.Scan(&d.ID, &d.Name, &d.LastSeenAt, &d.RevokedAt, &d.CreatedAt)
	})
}

// RevokeDevice revokes an active device by name.
func (db *DB) RevokeDevice(ctx context.Context, name string, now time.Time) error {
	tag, err := db.Pool.Exec(ctx,
		`update devices set revoked_at=$2 where name=$1 and revoked_at is null`, name, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthDevice finds the active device for a raw token and updates last_seen_at.
func (db *DB) AuthDevice(ctx context.Context, token string, now time.Time) (Device, error) {
	var d Device
	err := db.Pool.QueryRow(ctx,
		`update devices set last_seen_at=$2 where token_hash=$1 and revoked_at is null
		 returning id, name, last_seen_at, revoked_at, created_at`,
		HashToken(token), now).Scan(&d.ID, &d.Name, &d.LastSeenAt, &d.RevokedAt, &d.CreatedAt)
	return d, notFound(err)
}

// LastSeenDevice returns the most recently seen active device (ErrNotFound if none).
func (db *DB) LastSeenDevice(ctx context.Context) (Device, error) {
	var d Device
	err := db.Pool.QueryRow(ctx,
		`select id, name, last_seen_at, revoked_at, created_at from devices
		 where revoked_at is null and last_seen_at is not null order by last_seen_at desc limit 1`,
	).Scan(&d.ID, &d.Name, &d.LastSeenAt, &d.RevokedAt, &d.CreatedAt)
	return d, notFound(err)
}
