// Package bot implements the owner-only Telegram bot.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/agentstatus"
	"github.com/ardwiinoo/tunas/internal/reminders"
	"github.com/ardwiinoo/tunas/internal/store"
)

// MaxTitleLen bounds task titles (runes).
const MaxTitleLen = 200

// listLimit bounds list replies.
const listLimit = 50

// Commands dispatches bot commands. It is transport-independent.
type Commands struct {
	DB  *store.DB
	Loc *time.Location
	Now func() time.Time
	Log *slog.Logger
	// Extra handles commands added by later phases (/today, /tanya, /form).
	// It returns handled=false for unknown commands.
	Extra func(ctx context.Context, cmd, args string) (reply string, handled bool)
}

const helpText = `Halo, aku Tunas 🌱
/task <judul> — tambah tugas
/tasks — tugas terbuka
/done <id> — tandai selesai
/remind <waktu> <teks> — buat pengingat
/reminders — pengingat tertunda
/cancel <id> — batalkan pengingat
/today — ringkasan aktivitas hari ini
/tanya <pertanyaan> — tanya data aktivitas
/form <id>|auto — atur form (biasa, fokus, tidur, senang, ingat, tegur)
/cari <kueri> — cari di web (dengan sumber)
/export — unduh semua data (JSON.gz)
/log [kemarin|YYYY-MM-DD] <teks> — catat worklog
/cal [YYYY-MM] — kalender worklog (ketuk tanggal untuk lihat/ubah)
/libur [YYYY-MM-DD] — tandai libur (tanpa pengingat); /kerja [YYYY-MM-DD] — tandai hari kerja
/worklog [on|off|jam 10:30,13:30,16:30|penutup 17:30] — pengingat worklog
/status — status agen laptop (online/offline, versi, event tertunda)
/settings — pengaturan (mis. kabar laptop offline/online)
/ping — cek hidup

Bisa juga kirim kalimat biasa, mis. "ingatkan aku jam 5 sore kirim laporan".`

// SplitCommand parses "/cmd@Bot args" into ("cmd", "args"). ok=false if not a command.
func SplitCommand(text string) (cmd, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text, " ")
	head, _, _ = strings.Cut(head[1:], "@")
	return strings.ToLower(head), strings.TrimSpace(rest), head != ""
}

// Handle returns the reply for a message from the owner.
func (c *Commands) Handle(ctx context.Context, text string) string {
	cmd, args, ok := SplitCommand(text)
	if !ok {
		return "Ketik /help untuk daftar perintah."
	}
	reply, err := c.dispatch(ctx, cmd, args)
	if err != nil {
		c.Log.Error("command failed", "cmd", cmd, "err", err)
		return "Maaf, ada gangguan di server. Coba lagi sebentar."
	}
	return reply
}

func (c *Commands) dispatch(ctx context.Context, cmd, args string) (string, error) {
	switch cmd {
	case "start", "help":
		return helpText, nil
	case "task":
		return c.addTask(ctx, args)
	case "tasks":
		return c.listTasks(ctx)
	case "done":
		return c.doneTask(ctx, args)
	case "remind":
		return c.addReminder(ctx, args)
	case "reminders":
		return c.listReminders(ctx)
	case "cancel":
		return c.cancelReminder(ctx, args)
	case "ping":
		return c.ping(ctx)
	case "today":
		return c.today(ctx)
	case "status":
		return c.status(ctx)
	case "settings":
		return c.settings(ctx, args)
	}
	if c.Extra != nil {
		if reply, ok := c.Extra(ctx, cmd, args); ok {
			return reply, nil
		}
	}
	return "Perintah tidak dikenal. Ketik /help.", nil
}

func (c *Commands) addTask(ctx context.Context, title string) (string, error) {
	if title == "" {
		return "Pakai: /task <judul>", nil
	}
	if utf8.RuneCountInString(title) > MaxTitleLen {
		return fmt.Sprintf("Judul terlalu panjang (maks %d karakter).", MaxTitleLen), nil
	}
	t, err := c.DB.CreateTask(ctx, title)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Tugas #%d dicatat: %s", t.ID, t.Title), nil
}

func (c *Commands) listTasks(ctx context.Context) (string, error) {
	ts, err := c.DB.OpenTasks(ctx, listLimit)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "Tidak ada tugas terbuka. 🎉", nil
	}
	var b strings.Builder
	b.WriteString("Tugas terbuka:")
	for _, t := range ts {
		fmt.Fprintf(&b, "\n%d. %s", t.ID, t.Title)
	}
	return b.String(), nil
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(s), "#"), 10, 64)
	return id, err == nil && id > 0
}

func (c *Commands) doneTask(ctx context.Context, args string) (string, error) {
	id, ok := parseID(args)
	if !ok {
		return "Pakai: /done <id>", nil
	}
	t, err := c.DB.CompleteTask(ctx, id, c.Now())
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("Tugas #%d tidak ada atau sudah selesai.", id), nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("🎉 Selesai: %s", t.Title), nil
}

func (c *Commands) addReminder(ctx context.Context, args string) (string, error) {
	now := c.Now()
	p, err := reminders.Parse(args, now, c.Loc)
	if err != nil {
		return "Tidak bisa membaca waktu: " + err.Error() + "\n\n" + reminders.Usage, nil
	}
	r, err := c.DB.CreateReminder(ctx, p.Text, p.At, p.Recurrence)
	if err != nil {
		return "", err
	}
	reply := fmt.Sprintf("Oke, diingatkan %s: %s", reminders.FormatTime(r.RemindAt, now, c.Loc), r.Text)
	if d := reminders.DescribeRecurrence(r.Recurrence); d != "" {
		reply += " (" + d + ")"
	}
	return reply + fmt.Sprintf(" [#%d]", r.ID), nil
}

func (c *Commands) listReminders(ctx context.Context) (string, error) {
	rs, err := c.DB.PendingReminders(ctx, listLimit)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "Tidak ada pengingat tertunda.", nil
	}
	now := c.Now()
	var b strings.Builder
	b.WriteString("Pengingat tertunda:")
	for _, r := range rs {
		fmt.Fprintf(&b, "\n%d. %s: %s", r.ID, reminders.FormatTime(r.RemindAt, now, c.Loc), r.Text)
		if d := reminders.DescribeRecurrence(r.Recurrence); d != "" {
			b.WriteString(" (" + d + ")")
		}
	}
	return b.String(), nil
}

func (c *Commands) cancelReminder(ctx context.Context, args string) (string, error) {
	id, ok := parseID(args)
	if !ok {
		return "Pakai: /cancel <id>", nil
	}
	r, err := c.DB.CancelReminder(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Sprintf("Pengingat #%d tidak ada atau sudah tidak tertunda.", id), nil
	}
	if err != nil {
		return "", err
	}
	return "🗑️ Dibatalkan: " + r.Text, nil
}

func (c *Commands) ping(ctx context.Context) (string, error) {
	now := c.Now()
	reply := "Pong 🌱\nWaktu server: " + reminders.FormatTime(now, now, c.Loc)
	d, err := c.DB.LastSeenDevice(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		reply += "\nPerangkat: belum ada yang terhubung."
	case err != nil:
		return "", err
	default:
		reply += fmt.Sprintf("\nPerangkat %s terakhir terlihat %s lalu.", d.Name, ago(now.Sub(*d.LastSeenAt)))
	}
	return reply, nil
}

func (c *Commands) status(ctx context.Context) (string, error) {
	devs, err := c.DB.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	now := c.Now()
	var parts []string
	for _, d := range devs {
		if d.RevokedAt != nil {
			continue
		}
		pending, err := c.DB.PendingEvents(ctx, d.ID)
		if err != nil {
			return "", err
		}
		parts = append(parts, agentstatus.Describe(d, pending, now, c.Loc))
	}
	if len(parts) == 0 {
		return "Belum ada perangkat. Buat dulu di server: tunas-server device create --name laptop", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// settingDefs are the switches /settings can change.
var settingDefs = []struct{ name, key, desc string }{
	{"notif_status", agentstatus.SettingNotify, "kabar saat laptop offline > 15 menit / kembali online"},
}

func (c *Commands) settings(ctx context.Context, args string) (string, error) {
	f := strings.Fields(strings.ToLower(args))
	if len(f) == 0 {
		var b strings.Builder
		b.WriteString("Pengaturan:")
		for _, d := range settingDefs {
			on := true
			if _, err := c.DB.GetSetting(ctx, d.key, &on); err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\n• %s: %s — %s", d.name, onOff(on), d.desc)
		}
		b.WriteString("\nUbah: /settings <nama> on|off, mis. /settings notif_status off")
		return b.String(), nil
	}
	for _, d := range settingDefs {
		if f[0] != d.name {
			continue
		}
		if len(f) != 2 || (f[1] != "on" && f[1] != "off") {
			return fmt.Sprintf("Pakai: /settings %s on|off", d.name), nil
		}
		on := f[1] == "on"
		if err := c.DB.SetSetting(ctx, d.key, on); err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ %s sekarang %s.", d.name, onOff(on)), nil
	}
	return "Pengaturan tidak dikenal. Ketik /settings untuk daftarnya.", nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "kurang dari 1 menit"
	case d < time.Hour:
		return fmt.Sprintf("%d menit", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d jam", int(d.Hours()))
	}
	return fmt.Sprintf("%d hari", int(d.Hours()/24))
}

func (c *Commands) today(ctx context.Context) (string, error) {
	now := c.Now()
	from, to := activity.DayRange(now, c.Loc)
	bs, err := c.DB.BucketsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	title := "📊 Hari ini (" + reminders.FormatDate(now, now, c.Loc) + ")"
	return activity.FormatToday(activity.Summarize(bs), title, c.Loc), nil
}
