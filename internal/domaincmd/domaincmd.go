// Package domaincmd is the owner's view of website time from Telegram
// (PRD Fase 8c): /domain, the category of a site, block and forget, and the
// one-time question about a new site that has been open for a while.
package domaincmd

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/domains"
	"github.com/ardwiinoo/tunas/internal/projreport"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Settings keys.
const (
	KeyNotify      = "domains.notify"      // new-site questions, default true
	KeyNotified    = "domains.notified"    // []string already asked about
	KeyIncludeLLM  = "llm.include_domains" // domains may reach the LLM (default off)
	callbackPrefix = "dm:"
)

// Tuning.
const (
	NoticeMin   = 15 * time.Minute
	NoticeEvery = 5 * time.Minute
	maxNotice   = 3
	listLimit   = 12
)

// Button, Card: a message with inline buttons.
type Button struct{ Text, Data string }

// Card is a message with an optional keyboard.
type Card struct {
	Text  string
	Rows  [][]Button
	Toast string
}

// Service answers the domain commands.
type Service struct {
	DB   *store.DB
	Now  func() time.Time
	Loc  *time.Location
	Log  *slog.Logger
	Send func(ctx context.Context, c Card) error
}

// SeedCategories is the starting category of well-known sites; the owner
// changes them with /domain. Matching is by suffix.
func SeedCategories() map[string]string {
	m := map[string]string{}
	for _, d := range []string{"youtube.com", "youtu.be", "x.com", "twitter.com", "instagram.com", "tiktok.com", "netflix.com",
		"facebook.com", "twitch.tv", "9gag.com", "disneyplus.com", "hotstar.com"} {
		m[d] = "distract"
	}
	for _, d := range []string{"github.com", "gitlab.com", "stackoverflow.com", "stackexchange.com", "developer.mozilla.org", "go.dev",
		"pkg.go.dev", "docs.docker.com", "kubernetes.io", "postgresql.org"} {
		m[d] = "work"
	}
	return m
}

// Seed stores the starting categories for domains that have none.
func (s *Service) Seed(ctx context.Context) (int, error) {
	return s.DB.SeedDomainCategories(ctx, SeedCategories(), s.Now())
}

var cats = []struct{ code, category, label string }{
	{"k", "work", "Kerja"}, {"b", "browse", "Browse"}, {"h", "distract", "Hiburan"}, {"l", "other", "Lainnya"},
}

var catWords = map[string]string{
	"work": "work", "kerja": "work", "browse": "browse", "jelajah": "browse",
	"distract": "distract", "hiburan": "distract", "other": "other", "lainnya": "other",
}

func label(c string) string { return activity.CategoryLabel(c) }

func (s *Service) rangeOf(word string) (time.Time, time.Time, string) {
	start, end := activity.DayRange(s.Now(), s.Loc)
	if word == "minggu" {
		return start.AddDate(0, 0, -6), end, "7 hari terakhir"
	}
	return start, end, "hari ini"
}

// Section renders the top domains for /today ("" when there is no data).
func Section(ts []store.DomainTotal, limit int) string {
	if len(ts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Situs teratas:")
	for i, t := range ts {
		if i == limit {
			break
		}
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, t.Domain, projreport.Compact(time.Duration(t.Seconds)*time.Second))
	}
	return b.String()
}

const usage = "Pakai: /domain [hari|minggu] · /domain <situs> <kerja|browse|hiburan|lainnya> · /domain blok|buka|hapus <situs>"

// Command handles /domain.
func (s *Service) Command(ctx context.Context, args string) (string, error) {
	f := strings.Fields(args)
	if len(f) == 0 || f[0] == "hari" || f[0] == "minggu" {
		word := ""
		if len(f) > 0 {
			word = f[0]
		}
		return s.report(ctx, word)
	}
	switch strings.ToLower(f[0]) {
	case "blok", "buka", "hapus":
		return s.admin(ctx, strings.ToLower(f[0]), f[1:])
	case "notif":
		if len(f) == 2 && (f[1] == "on" || f[1] == "off") {
			return "✅ Kabar situs baru sekarang " + f[1] + ".", s.DB.SetSetting(ctx, KeyNotify, f[1] == "on")
		}
		return usage, nil
	}
	if len(f) == 2 {
		return s.setCategory(ctx, f[0], f[1])
	}
	return usage, nil
}

func (s *Service) report(ctx context.Context, word string) (string, error) {
	from, to, lbl := s.rangeOf(word)
	ts, err := s.DB.DomainTotalsBetween(ctx, from, to)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "Belum ada data situs untuk " + lbl + ". Pasang ekstensi browser dan nyalakan \"Catat domain situs\" di menu tray laptop (lihat DEPLOY).", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🌐 Situs %s (hanya nama domain)", lbl)
	for i, t := range ts {
		if i == listLimit {
			fmt.Fprintf(&b, "\n… dan %d situs lain", len(ts)-i)
			break
		}
		cat := "belum dikategorikan"
		mark := "? "
		if t.Category != "" {
			cat, mark = label(t.Category), ""
		}
		fmt.Fprintf(&b, "\n%s%s — %s · %s", mark, t.Domain, projreport.Compact(time.Duration(t.Seconds)*time.Second), cat)
	}
	b.WriteString("\nUbah: /domain <situs> <kategori>")
	return b.String(), nil
}

func (s *Service) setCategory(ctx context.Context, site, word string) (string, error) {
	d, ok := domains.Normalize(site)
	cat := catWords[strings.ToLower(word)]
	if !ok || cat == "" {
		return usage, nil
	}
	if err := s.DB.SetDomainCategory(ctx, d, cat, s.Now()); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ %s (dan subdomainnya) masuk kategori %s.", d, label(cat)), nil
}

func (s *Service) admin(ctx context.Context, verb string, f []string) (string, error) {
	if verb == "blok" && len(f) == 0 {
		bl, err := s.DB.DomainBlocklist(ctx)
		if err != nil {
			return "", err
		}
		if len(bl) == 0 {
			return "Tidak ada situs yang diblok. Pakai: /domain blok <situs>", nil
		}
		return "Situs yang tidak dicatat: " + strings.Join(bl, ", "), nil
	}
	if len(f) != 1 {
		return usage, nil
	}
	d, ok := domains.Normalize(f[0])
	if !ok {
		return usage, nil
	}
	switch verb {
	case "blok":
		n, err := s.DB.BlockDomain(ctx, d)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ %s tidak akan dicatat lagi (%d catatan lama dihapus). Laptop mengikutinya dalam ≤ 2 menit.", d, n), nil
	case "buka":
		return fmt.Sprintf("✅ %s boleh dicatat lagi.", d), s.DB.UnblockDomain(ctx, d)
	}
	n, err := s.DB.DeleteDomainHistory(ctx, d)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Riwayat %s dihapus (%d catatan).", d, n), nil
}

// Callback handles the category buttons of the new-site question.
func (s *Service) Callback(ctx context.Context, data string) (Card, bool, error) {
	rest, ok := strings.CutPrefix(data, callbackPrefix)
	if !ok {
		return Card{}, false, nil
	}
	code, host, found := strings.Cut(rest, ":")
	category := ""
	for _, c := range cats {
		if c.code == code {
			category = c.category
		}
	}
	d, valid := domains.Normalize(host)
	if !found || category == "" || !valid {
		return Card{Toast: "Tombol tidak dikenal."}, true, nil
	}
	if err := s.DB.SetDomainCategory(ctx, d, category, s.Now()); err != nil {
		return Card{}, true, err
	}
	msg := d + " → " + label(category)
	return Card{Text: "✅ " + msg + ".", Toast: msg}, true, nil
}

// Notice asks once about each site that was open for NoticeMin without a category.
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
	for _, d := range done {
		seen[d] = true
	}
	use, err := s.DB.UncategorizedDomains(ctx, int(NoticeMin.Seconds()))
	if err != nil {
		return err
	}
	sent := 0
	for _, u := range use {
		if seen[u.Domain] || sent == maxNotice {
			continue
		}
		card := Card{Text: fmt.Sprintf("Situs baru: %s (%s). Kategorinya?", u.Domain, strings.Replace(projreport.Compact(time.Duration(u.Seconds)*time.Second), "m", " mnt", 1))}
		var row []Button
		for _, c := range cats {
			if d := callbackPrefix + c.code + ":" + u.Domain; len(d) <= 64 {
				row = append(row, Button{Text: c.label, Data: d})
			}
		}
		if len(row) > 0 {
			card.Rows = [][]Button{row}
		} else {
			card.Text += " Pakai /domain " + u.Domain + " <kategori>."
		}
		if err := s.Send(ctx, card); err != nil {
			return err
		}
		done = append(done, u.Domain)
		if err := s.DB.SetSetting(ctx, KeyNotified, done); err != nil {
			return err
		}
		sent++
	}
	return nil
}

// Run checks for new sites until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(NoticeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Notice(ctx); err != nil && s.Log != nil {
				s.Log.Warn("new site notice failed", "err", err)
			}
		}
	}
}
