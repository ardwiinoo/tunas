// Package apps is the owner's control of the app → category mapping from
// Telegram (PRD Fase 8b): /apps, /kategori and the one-time notice about a
// new app that has been used for a while without a category.
package apps

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Settings keys.
const (
	KeyNotify   = "apps.notify"   // bool, default true
	KeyNotified = "apps.notified" // []string of lower-case apps already announced
)

// Tuning.
const (
	NoticeMin      = 15 * time.Minute // total use of an uncategorized app that triggers the notice
	MaxRows        = 8                // apps with buttons in /apps (Telegram allows 100 buttons)
	NoticeEvery    = 5 * time.Minute
	maxNoticePerGo = 3
	callbackPrefix = "ap:"
)

// Button and Card are a message with inline buttons.
type Button struct{ Text, Data string }

// Card is a message with an optional keyboard.
type Card struct {
	Text  string
	Rows  [][]Button
	Toast string // callbacks only
}

// Service reads and changes the mapping.
type Service struct {
	DB   *store.DB
	Now  func() time.Time
	Loc  *time.Location
	Log  *slog.Logger
	Send func(ctx context.Context, c Card) error // the notice; nil = no notices
}

var cats = []struct{ code, category, label string }{
	{"k", "work", "Kerja"}, {"b", "browse", "Browse"}, {"h", "distract", "Hiburan"}, {"l", "other", "Lainnya"},
}

var catWords = map[string]string{
	"work": "work", "kerja": "work", "browse": "browse", "jelajah": "browse",
	"distract": "distract", "hiburan": "distract", "other": "other", "lainnya": "other",
}

// reApp accepts the names Windows gives executables, nothing with a path.
var reApp = regexp.MustCompile(`^[\p{L}\p{N} ._+()~-]{1,100}$`)

// NormalizeApp lower-cases a name and adds ".exe" when there is no extension.
func NormalizeApp(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.Trim(s, ". ") == "" {
		return "", false
	}
	if !strings.Contains(s, ".") {
		s += ".exe"
	}
	if !reApp.MatchString(s) {
		return "", false
	}
	return s, true
}

// CategoryName is the Indonesian label of a category.
func CategoryName(c string) string { return activity.CategoryLabel(c) }

// Seed stores the starting mapping for apps that have none yet.
func (s *Service) Seed(ctx context.Context, seed map[string]string) (int, error) {
	return s.DB.SeedAppCategories(ctx, seed, s.Now())
}

func dur(sec int) string { return activity.FormatDuration(time.Duration(sec) * time.Second) }

func (s *Service) rangeOf(period string) (from, to time.Time, label string) {
	start, end := activity.DayRange(s.Now(), s.Loc)
	if period == "m" {
		return start.AddDate(0, 0, -6), end, "7 hari terakhir"
	}
	return start, end, "hari ini"
}

// List renders /apps for a period ("h" today, "m" the last 7 days).
func (s *Service) List(ctx context.Context, period string) (Card, error) {
	from, to, label := s.rangeOf(period)
	use, err := s.DB.AppUsageBetween(ctx, from, to)
	if err != nil {
		return Card{}, err
	}
	if len(use) == 0 {
		return Card{Text: "Belum ada aktivitas aplikasi untuk " + label + "."}, nil
	}
	// uncategorized first, then by use
	var order []store.AppUsage
	for _, u := range use {
		if u.Category == "" {
			order = append(order, u)
		}
	}
	for _, u := range use {
		if u.Category != "" {
			order = append(order, u)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📱 Aplikasi %s (? = belum dikategorikan)", label)
	card := Card{}
	for i, u := range order {
		mark := ""
		if u.Category == "" {
			mark = "? "
		}
		cat := "belum dikategorikan"
		if u.Category != "" {
			cat = CategoryName(u.Category)
		}
		if i >= 15 {
			fmt.Fprintf(&b, "\n… dan %d aplikasi lain (pakai /kategori <app> <kategori>)", len(order)-i)
			break
		}
		fmt.Fprintf(&b, "\n%s%s — %s · %s", mark, u.App, dur(u.Seconds), cat)
		if i < MaxRows {
			if row := s.buttons(period, u); row != nil {
				card.Rows = append(card.Rows, []Button{{Text: "▸ " + u.App, Data: callbackPrefix + "n"}}, row)
			}
		}
	}
	b.WriteString("\nUbah: ketuk tombol, atau /kategori <app> <kerja|browse|hiburan|lainnya>.")
	card.Text = b.String()
	return card, nil
}

// buttons is the category row of an app (nil when its name is too long for
// callback data).
func (s *Service) buttons(period string, u store.AppUsage) []Button {
	app := strings.ToLower(u.App)
	var row []Button
	for _, c := range cats {
		data := callbackPrefix + period + ":" + c.code + ":" + app
		if len(data) > 64 {
			return nil
		}
		text := c.label
		if c.category == u.Category {
			text = "✔ " + text
		}
		row = append(row, Button{Text: text, Data: data})
	}
	return row
}

// Set changes one app's category. category may be a word in either language.
func (s *Service) Set(ctx context.Context, app, category string) (string, error) {
	name, ok := NormalizeApp(app)
	cat := catWords[strings.ToLower(strings.TrimSpace(category))]
	if !ok || cat == "" {
		return "Pakai: /kategori <app> <kerja|browse|hiburan|lainnya>, mis. /kategori Antigravity.exe kerja", nil
	}
	if err := s.DB.SetAppCategory(ctx, name, cat, s.Now()); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ %s sekarang masuk kategori %s. Laporan hari-hari lalu ikut berubah.", name, CategoryName(cat)), nil
}

// Command handles "/apps ..." and "/kategori ...". ok=false for other commands.
func (s *Service) Command(ctx context.Context, cmd, args string) (Card, bool, error) {
	switch cmd {
	case "apps":
		f := strings.Fields(strings.ToLower(args))
		switch {
		case len(f) == 0 || f[0] == "hari":
			c, err := s.List(ctx, "h")
			return c, true, err
		case f[0] == "minggu":
			c, err := s.List(ctx, "m")
			return c, true, err
		case f[0] == "notif" && len(f) == 2 && (f[1] == "on" || f[1] == "off"):
			if err := s.DB.SetSetting(ctx, KeyNotify, f[1] == "on"); err != nil {
				return Card{}, true, err
			}
			return Card{Text: "✅ Kabar aplikasi baru sekarang " + f[1] + "."}, true, nil
		}
		return Card{Text: "Pakai: /apps [hari|minggu] atau /apps notif on|off"}, true, nil
	case "kategori":
		f := strings.Fields(args)
		if len(f) < 2 {
			return Card{Text: "Pakai: /kategori <app> <kerja|browse|hiburan|lainnya>, mis. /kategori Antigravity.exe kerja"}, true, nil
		}
		text, err := s.Set(ctx, strings.Join(f[:len(f)-1], " "), f[len(f)-1])
		return Card{Text: text}, true, err
	}
	return Card{}, false, nil
}

// Callback handles a button. ok=false when data is not an apps button.
func (s *Service) Callback(ctx context.Context, data string) (Card, bool, error) {
	rest, ok := strings.CutPrefix(data, callbackPrefix)
	if !ok {
		return Card{}, false, nil
	}
	if rest == "n" { // the app's name label
		return Card{}, true, nil
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 {
		return Card{Toast: "Tombol tidak dikenal."}, true, nil
	}
	period, code, app := parts[0], parts[1], parts[2]
	category := ""
	for _, c := range cats {
		if c.code == code {
			category = c.category
		}
	}
	name, ok := NormalizeApp(app)
	if category == "" || !ok {
		return Card{Toast: "Tombol tidak dikenal."}, true, nil
	}
	if err := s.DB.SetAppCategory(ctx, name, category, s.Now()); err != nil {
		return Card{}, true, err
	}
	toast := name + " → " + CategoryName(category)
	if period == "x" { // the notice: confirm and drop the buttons
		return Card{Text: "✅ " + toast + ".", Toast: toast}, true, nil
	}
	c, err := s.List(ctx, period)
	c.Toast = toast
	return c, true, err
}

// Notice announces apps that were used for NoticeMin without a category,
// once per app. It does nothing when notices are off or there is no Send.
func (s *Service) Notice(ctx context.Context) error {
	if s.Send == nil {
		return nil
	}
	on := true
	if _, err := s.DB.GetSetting(ctx, KeyNotify, &on); err != nil || !on {
		return err
	}
	var done []string
	if _, err := s.DB.GetSetting(ctx, KeyNotified, &done); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range done {
		seen[a] = true
	}
	use, err := s.DB.UncategorizedApps(ctx, int(NoticeMin.Seconds()))
	if err != nil {
		return err
	}
	sent := 0
	for _, u := range use {
		key := strings.ToLower(u.App)
		if seen[key] {
			continue
		}
		if sent == maxNoticePerGo {
			break
		}
		card := Card{Text: fmt.Sprintf("Aplikasi baru: %s (%s). Kategorinya?", u.App, strings.Replace(dur(u.Seconds), " m", " mnt", 1))}
		var row []Button
		for _, c := range cats {
			if data := callbackPrefix + "x:" + c.code + ":" + key; len(data) <= 64 {
				row = append(row, Button{Text: c.label, Data: data})
			}
		}
		if len(row) > 0 {
			card.Rows = [][]Button{row}
		} else {
			card.Text += " Pakai /kategori " + u.App + " <kategori>."
		}
		if err := s.Send(ctx, card); err != nil {
			return err // retried next time; nothing was recorded
		}
		done = append(done, key)
		if err := s.DB.SetSetting(ctx, KeyNotified, done); err != nil {
			return err
		}
		sent++
	}
	return nil
}

// Run checks for new apps until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(NoticeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Notice(ctx); err != nil && s.Log != nil {
				s.Log.Warn("new app notice failed", "err", err)
			}
		}
	}
}
