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
/ping — cek hidup`

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
