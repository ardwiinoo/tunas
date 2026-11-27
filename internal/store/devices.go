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

// ErrExpired is returned by AuthDeviceIP for a token past its expiry.
var ErrExpired = errors.New("device token expired")

// DefaultTokenDays is how long a device token is valid (PRD 9b).
const DefaultTokenDays = 180

// ExpiryWarnDays is how long before expiry the owner is warned (once).
const ExpiryWarnDays = 14

// Device is a registered agent.
type Device struct {
	ID             int64
	Name           string
	LastSeenAt     *time.Time // any authenticated request
	RevokedAt      *time.Time
	CreatedAt      time.Time
	AgentVersion   string     // from the last heartbeat
	TrackingPaused bool       // from the last heartbeat
	HeartbeatAt    *time.Time // last heartbeat
	ExpiresAt      *time.Time // the token stops working then (nil: never)
	LastSeenIP     string     // client address of the last authenticated request ("" = none yet)
}

const deviceCols = `d.id, d.name, d.last_seen_at, d.revoked_at, d.created_at, d.agent_version, d.tracking_paused, d.heartbeat_at, d.expires_at, coalesce(d.last_seen_ip, '')`

func scanDevice(r pgx.Row, d *Device, extra ...any) error {
	dst := append([]any{&d.ID, &d.Name, &d.LastSeenAt, &d.RevokedAt, &d.CreatedAt, &d.AgentVersion, &d.TrackingPaused, &d.HeartbeatAt, &d.ExpiresAt, &d.LastSeenIP}, extra...)
	return r.Scan(dst...)
}

// HashToken returns the hex sha256 of a raw device token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateDevice registers a device with the default token lifetime and returns
// its raw token (shown once; only the hash is stored).
func (db *DB) CreateDevice(ctx context.Context, name string) (Device, string, error) {
	return db.CreateDeviceDays(ctx, name, DefaultTokenDays)
}

// CreateDeviceDays is CreateDevice with an explicit lifetime in days.
func (db *DB) CreateDeviceDays(ctx context.Context, name string, days int) (Device, string, error) {
	if days <= 0 {
		return Device{}, "", errors.New("token lifetime must be at least 1 day")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Device{}, "", err
	}
	token := "tnd_" + base64.RawURLEncoding.EncodeToString(buf)
	var d Device
	err := db.Pool.QueryRow(ctx,
		`insert into devices(name, token_hash, expires_at) values($1, $2, now() + make_interval(days => $3))
		 returning id, name, created_at, expires_at`,
		name, HashToken(token), days).Scan(&d.ID, &d.Name, &d.CreatedAt, &d.ExpiresAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Device{}, "", ErrExists
	}
	return d, token, err
}

// ListDevices returns all devices.
func (db *DB) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := db.Pool.Query(ctx, `select `+deviceCols+` from devices d order by d.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Device, error) {
		var d Device
		return d, scanDevice(r, &d)
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

// RevokeDeviceID revokes an active device by id and returns its name.
func (db *DB) RevokeDeviceID(ctx context.Context, id int64, now time.Time) (string, error) {
	var name string
	err := db.Pool.QueryRow(ctx,
		`update devices set revoked_at=$2 where id=$1 and revoked_at is null returning name`, id, now).Scan(&name)
	return name, notFound(err)
}

// DeviceByID returns a device (ErrNotFound if none).
func (db *DB) DeviceByID(ctx context.Context, id int64) (Device, error) {
	var d Device
	err := scanDevice(db.Pool.QueryRow(ctx, `select `+deviceCols+` from devices d where d.id=$1`, id), &d)
	return d, notFound(err)
}

// AuthDevice finds the active device for a raw token and updates last_seen_at.
// It is AuthDeviceIP without a client address.
func (db *DB) AuthDevice(ctx context.Context, token string, now time.Time) (Device, error) {
	d, _, err := db.AuthDeviceIP(ctx, token, now, "")
	return d, err
}

// AuthDeviceIP finds the active, unexpired device for a raw token (matched
// through its sha256 hash, never compared as text), records the last seen
// time and, when ip is not empty, the client address. It also returns the
// address of the request before this one ("" if none). An expired token is
// ErrExpired, an unknown or revoked one ErrNotFound.
func (db *DB) AuthDeviceIP(ctx context.Context, token string, now time.Time, ip string) (Device, string, error) {
	var d Device
	var prev string
	err := scanDevice(db.Pool.QueryRow(ctx, `
		with cur as (
			select id, coalesce(last_seen_ip, '') as prev from devices
			where token_hash = $1 and revoked_at is null and (expires_at is null or expires_at > $2)
		)
		update devices d set last_seen_at = $2, last_seen_ip = case when $3::text = '' then d.last_seen_ip else $3::text end
		from cur where d.id = cur.id
		returning `+deviceCols+`, cur.prev`, HashToken(token), now, ip), &d, &prev)
	if err == nil {
		return d, prev, nil
	}
	if !errors.Is(notFound(err), ErrNotFound) {
		return Device{}, "", err
	}
	var expired bool
	if e := db.Pool.QueryRow(ctx, `select exists(select 1 from devices where token_hash=$1 and revoked_at is null and expires_at <= $2)`,
		HashToken(token), now).Scan(&expired); e == nil && expired {
		return Device{}, "", ErrExpired
	}
	return Device{}, "", ErrNotFound
}

// ClaimIPAlert returns true if a new-address warning for the device may be
// sent now (at most one per 24 hours) and records that it is being sent.
func (db *DB) ClaimIPAlert(ctx context.Context, id int64, now time.Time) (bool, error) {
	tag, err := db.Pool.Exec(ctx, `update devices set ip_alerted_at = $2
		where id = $1 and (ip_alerted_at is null or ip_alerted_at <= $2::timestamptz - interval '24 hours')`, id, now)
	return tag.RowsAffected() == 1, err
}

// ClaimExpiryWarning returns true once per device when its token is within
// ExpiryWarnDays of expiring (and not yet expired).
func (db *DB) ClaimExpiryWarning(ctx context.Context, id int64, now time.Time) (bool, error) {
	tag, err := db.Pool.Exec(ctx, `update devices set expiry_warned_at = $2
		where id = $1 and expiry_warned_at is null and expires_at is not null
		  and expires_at > $2::timestamptz and expires_at <= $2::timestamptz + make_interval(days => $3)`, id, now, ExpiryWarnDays)
	return tag.RowsAffected() == 1, err
}

// LastSeenDevice returns the most recently seen active device (ErrNotFound if none).
func (db *DB) LastSeenDevice(ctx context.Context) (Device, error) {
	var d Device
	err := scanDevice(db.Pool.QueryRow(ctx,
		`select `+deviceCols+` from devices d
		 where d.revoked_at is null and d.last_seen_at is not null order by d.last_seen_at desc limit 1`), &d)
	return d, notFound(err)
}

// Heartbeat records the agent's version and tracking state.
func (db *DB) Heartbeat(ctx context.Context, deviceID int64, version string, paused bool, now time.Time) error {
	_, err := db.Pool.Exec(ctx,
		`update devices set agent_version=$2, tracking_paused=$3, heartbeat_at=$4, last_seen_at=$4 where id=$1`,
		deviceID, version, paused, now)
	return err
}

// PendingEvents counts events the device has not acknowledged yet.
func (db *DB) PendingEvents(ctx context.Context, deviceID int64) (int, error) {
	var n int
	err := db.Pool.QueryRow(ctx,
		`select count(*) from events where acked_at is null and (device_id is null or device_id = $1)`,
		deviceID).Scan(&n)
	return n, err
}
