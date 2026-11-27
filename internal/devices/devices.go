// Package devices is the owner's view of the registered agents (PRD 9b):
// /devices in Telegram with a confirmed revoke button, and the texts of the
// security warnings.
package devices

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/store"
)

// MaskIP hides the host part of an address for chat and log lines:
// 203.0.113.7 → 203.0.*.*, 2001:db8:1:2::9 → 2001:db8:*. Not an IP: "?".
func MaskIP(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return "?"
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.*.*", v4[0], v4[1])
	}
	return fmt.Sprintf("%x:%x:*", uint16(ip[0])<<8|uint16(ip[1]), uint16(ip[2])<<8|uint16(ip[3]))
}

// Callback data (Telegram allows 64 bytes).
const (
	cbAsk     = "dv:rv:" // + id: ask for confirmation
	cbConfirm = "dv:ok:" // + id: revoke
	cbCancel  = "dv:no"
)

// Service answers /devices.
type Service struct {
	DB  *store.DB
	Now func() time.Time
	Loc *time.Location
}

// Command is /devices.
func (s *Service) Command(ctx context.Context, args string) (bot.Reply, error) {
	if strings.TrimSpace(args) != "" {
		return bot.Reply{Text: "Pakai: /devices (tanpa tambahan)."}, nil
	}
	return s.list(ctx)
}

func (s *Service) list(ctx context.Context) (bot.Reply, error) {
	ds, err := s.DB.ListDevices(ctx)
	if err != nil {
		return bot.Reply{}, err
	}
	now := s.Now()
	var b strings.Builder
	var rows [][]bot.Button
	active := 0
	for _, d := range ds {
		if d.RevokedAt != nil {
			continue
		}
		active++
		fmt.Fprintf(&b, "%d. %s\n   terakhir dilihat: %s\n   IP: %s\n   token: %s\n",
			active, d.Name, seen(d, now), ipOrDash(d.LastSeenIP), expiry(d, now, s.Loc))
		rows = append(rows, []bot.Button{{Text: "Cabut " + d.Name, Data: cbAsk + strconv.FormatInt(d.ID, 10)}})
	}
	if active == 0 {
		return bot.Reply{Text: "Belum ada perangkat aktif.\nBuat token: tunas-server device create --name laptop"}, nil
	}
	b.WriteString("\nRotasi token: tunas-server device create --name <nama baru>, lalu tunas-agent.exe --set-token di laptop, lalu cabut perangkat lama di sini.")
	return bot.Reply{Text: fmt.Sprintf("Perangkat aktif (%d):\n%s", active, b.String()), Rows: rows}, nil
}

// Callback handles the buttons. ok is false for data that is not ours.
func (s *Service) Callback(ctx context.Context, data string) (bot.Reply, bool, error) {
	switch {
	case data == cbCancel:
		r, err := s.list(ctx)
		return r, true, err
	case strings.HasPrefix(data, cbAsk):
		id, err := strconv.ParseInt(strings.TrimPrefix(data, cbAsk), 10, 64)
		if err != nil {
			return bot.Reply{Toast: "Tombol tidak dikenal.", Keep: true}, true, nil
		}
		d, err := s.DB.DeviceByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && d.RevokedAt != nil) {
			return bot.Reply{Toast: "Perangkat itu sudah tidak aktif.", Keep: true}, true, nil
		}
		if err != nil {
			return bot.Reply{}, true, err
		}
		return bot.Reply{
			Text: fmt.Sprintf("Cabut perangkat %q?\nAgen di perangkat itu langsung ditolak server dan harus diberi token baru.", d.Name),
			Buttons: []bot.Button{
				{Text: "Ya, cabut", Data: cbConfirm + strconv.FormatInt(d.ID, 10)},
				{Text: "Batal", Data: cbCancel},
			},
		}, true, nil
	case strings.HasPrefix(data, cbConfirm):
		id, err := strconv.ParseInt(strings.TrimPrefix(data, cbConfirm), 10, 64)
		if err != nil {
			return bot.Reply{Toast: "Tombol tidak dikenal.", Keep: true}, true, nil
		}
		name, err := s.DB.RevokeDeviceID(ctx, id, s.Now())
		if errors.Is(err, store.ErrNotFound) {
			return bot.Reply{Toast: "Perangkat itu sudah tidak aktif.", Keep: true}, true, nil
		}
		if err != nil {
			return bot.Reply{}, true, err
		}
		return bot.Reply{Text: fmt.Sprintf("Perangkat %q dicabut. Token-nya tidak berlaku lagi.", name), Toast: "Dicabut."}, true, nil
	}
	return bot.Reply{}, false, nil
}

func ipOrDash(ip string) string {
	if ip == "" {
		return "-"
	}
	return MaskIP(ip)
}

func seen(d store.Device, now time.Time) string {
	if d.LastSeenAt == nil {
		return "belum pernah"
	}
	return ago(now.Sub(*d.LastSeenAt))
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "baru saja"
	case d < time.Hour:
		return fmt.Sprintf("%d mnt lalu", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d jam lalu", int(d.Hours()))
	}
	return fmt.Sprintf("%d hari lalu", int(d.Hours()/24))
}

func expiry(d store.Device, now time.Time, loc *time.Location) string {
	if d.ExpiresAt == nil {
		return "tanpa kedaluwarsa"
	}
	left := d.ExpiresAt.Sub(now)
	date := d.ExpiresAt.In(loc).Format("2006-01-02")
	if left <= 0 {
		return "KEDALUWARSA sejak " + date
	}
	return fmt.Sprintf("kedaluwarsa %s (%d hari lagi)", date, int(left.Hours()/24))
}

// NewIPText is the warning for a token used from a new address.
func NewIPText(name, prev, ip string) string {
	return fmt.Sprintf("Token perangkat %q dipakai dari alamat IP baru: %s (sebelumnya %s).\nKalau itu bukan kamu, cabut dengan /devices lalu buat token baru.", name, MaskIP(ip), ipOrDash(prev))
}

// FailuresText is the warning for repeated failed authentications.
func FailuresText(n int, window time.Duration, lastIP string) string {
	return fmt.Sprintf("%d percobaan autentikasi gagal ke server dalam %d menit (terakhir dari %s).\nKalau bukan kamu atau agenmu, ada yang mencoba menebak token. Tokenmu tidak ikut tercatat di log.", n, int(window.Minutes()), MaskIP(lastIP))
}

// ExpiryText is the one warning before a token expires.
func ExpiryText(name string, left time.Duration) string {
	return fmt.Sprintf("Token perangkat %q kedaluwarsa dalam %d hari.\nRotasi: tunas-server device create --name <nama baru>, lalu tunas-agent.exe --set-token di laptop, lalu cabut yang lama lewat /devices.", name, max(int(math.Round(left.Hours()/24)), 0))
}
