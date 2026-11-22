// Package agentstatus decides whether an agent is online and words it for
// the owner (/status and the online/offline notices).
package agentstatus

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/store"
)

// Timings. The agent sends a heartbeat every HeartbeatEvery and keeps a
// long-poll open, so a device unseen for OnlineWindow has stopped.
const (
	HeartbeatEvery = 60 * time.Second
	OnlineWindow   = 2*HeartbeatEvery + 30*time.Second
	NoticeAfter    = 15 * time.Minute // offline notice after this long
)

// Online reports whether the device was seen within OnlineWindow.
func Online(d store.Device, now time.Time) bool {
	return d.LastSeenAt != nil && now.Sub(*d.LastSeenAt) <= OnlineWindow
}

// Name is the device name for messages ("laptop" → "Laptop").
func Name(d store.Device) string {
	r, n := utf8.DecodeRuneInString(d.Name)
	if n == 0 {
		return "Perangkat"
	}
	return string(unicode.ToUpper(r)) + d.Name[n:]
}

// Clock renders t as "18:42", "kemarin 18:42" or "21 Nov 18:42".
func Clock(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	hm := t.Format("15:04")
	ty, tm, td := t.Date()
	ny, nm, nd := now.Date()
	switch {
	case ty == ny && tm == nm && td == nd:
		return hm
	case t.AddDate(0, 0, 1).Format("2006-01-02") == now.Format("2006-01-02"):
		return "kemarin " + hm
	}
	return fmt.Sprintf("%d %s %s", td, shortMonths[tm-1], hm)
}

var shortMonths = [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}

// Ago renders a duration as "12 detik", "5 menit", "2 jam 10 menit", "3 hari 4 jam".
func Ago(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d detik", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	case d < 48*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d jam", h)
		}
		return fmt.Sprintf("%d jam %d menit", h, m)
	}
	days, h := int(d.Hours())/24, int(d.Hours())%24
	if h == 0 {
		return fmt.Sprintf("%d hari", days)
	}
	return fmt.Sprintf("%d hari %d jam", days, h)
}

// Describe is the /status block for one device. pending is the number of
// events the agent has not acknowledged yet.
func Describe(d store.Device, pending int, now time.Time, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(Name(d) + ": ")
	switch {
	case d.LastSeenAt == nil:
		b.WriteString("belum pernah tersambung")
	case Online(d, now):
		fmt.Fprintf(&b, "online (terakhir terlihat %s lalu)", Ago(now.Sub(*d.LastSeenAt)))
	default:
		fmt.Fprintf(&b, "offline sejak %s (%s)", Clock(*d.LastSeenAt, now, loc), Ago(now.Sub(*d.LastSeenAt)))
	}
	v := d.AgentVersion
	if v == "" {
		v = "tidak diketahui"
	}
	fmt.Fprintf(&b, "\nVersi agen: %s", v)
	if d.TrackingPaused {
		b.WriteString("\nPelacakan: dijeda")
	}
	fmt.Fprintf(&b, "\nEvent tertunda: %d", pending)
	return b.String()
}

// SettingNotify is the settings key for the online/offline notices
// (JSON bool, default true). Toggled with /settings.
const SettingNotify = "notify_agent_status"

// SettingsStore is the part of store.DB the notice toggle needs.
type SettingsStore interface {
	GetSetting(ctx context.Context, key string, dst any) (bool, error)
}

// NotifyEnabled reports whether online/offline notices are on (default on).
func NotifyEnabled(ctx context.Context, db SettingsStore) (bool, error) {
	on := true
	if _, err := db.GetSetting(ctx, SettingNotify, &on); err != nil {
		return false, err
	}
	return on, nil
}
