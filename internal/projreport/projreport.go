// Package projreport turns project time (Fase 8f) into text: /proyek, the
// project part of /today and /tanya, and the building blocks of the
// end-of-day report. Only project names and editor names appear.
package projreport

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/projects"
	"github.com/ardwiinoo/tunas/internal/store"
)

// KeyIncludeLLM is the setting that lets project names reach the LLM (off by default).
const KeyIncludeLLM = "llm.include_projects"

// Compact formats a duration as "2j10m", "45m" or "<1m".
func Compact(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m <= 0:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m%60 == 0:
		return fmt.Sprintf("%dj", m/60)
	}
	return fmt.Sprintf("%dj%dm", m/60, m%60)
}

var editorLabels = map[string]string{"code.exe": "VS Code", "code - insiders.exe": "VS Code Insiders", "cursor.exe": "Cursor", "antigravity.exe": "Antigravity"}

// EditorLabel is a readable name for an editor's process name.
func EditorLabel(process string) string {
	p := strings.ToLower(process)
	if l, ok := editorLabels[p]; ok {
		return l
	}
	p = strings.TrimSuffix(p, ".exe")
	if p == "" {
		return process
	}
	return strings.ToUpper(p[:1]) + p[1:]
}

// Line renders one project: "project-A 2j10m (VS Code)", the editors in order of time.
func Line(t store.ProjectTotal) string {
	type ed struct {
		name string
		sec  int
	}
	var eds []ed
	for e, s := range t.Editors {
		eds = append(eds, ed{EditorLabel(e), s})
	}
	sort.Slice(eds, func(i, j int) bool {
		if eds[i].sec != eds[j].sec {
			return eds[i].sec > eds[j].sec
		}
		return eds[i].name < eds[j].name
	})
	names := make([]string, len(eds))
	for i, e := range eds {
		names[i] = e.name
	}
	return fmt.Sprintf("%s %s (%s)", t.Name, Compact(time.Duration(t.Seconds)*time.Second), strings.Join(names, ", "))
}

// Section renders the project lines for /today, "" when there is no data.
func Section(ts []store.ProjectTotal, limit int) string {
	if len(ts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Proyek:")
	for i, t := range ts {
		if i == limit {
			break
		}
		b.WriteString("\n• " + Line(t))
	}
	return b.String()
}

// Service answers the project commands.
type Service struct {
	DB  *store.DB
	Now func() time.Time
	Loc *time.Location
}

// Range returns the time range of "hari" (default), "minggu" (7 days) or "bulan".
func (s *Service) Range(word string) (from, to time.Time, label string, ok bool) {
	start, end := activity.DayRange(s.Now(), s.Loc)
	switch word {
	case "", "hari":
		return start, end, "hari ini", true
	case "minggu":
		return start.AddDate(0, 0, -6), end, "7 hari terakhir", true
	case "bulan":
		return time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, s.Loc), end, "bulan ini", true
	}
	return time.Time{}, time.Time{}, "", false
}

const usage = "Pakai: /proyek [hari|minggu|bulan] · /proyek alias <nama> <alias> · /proyek blok <nama> · /proyek buka <nama> · /proyek hapus <nama>"

// Totals returns the project totals of a range.
func (s *Service) Totals(ctx context.Context, from, to time.Time) ([]store.ProjectTotal, error) {
	return s.DB.ProjectTotalsBetween(ctx, from, to)
}

// Command handles /proyek.
func (s *Service) Command(ctx context.Context, args string) (string, error) {
	f := splitArgs(args)
	if len(f) == 0 {
		return s.report(ctx, "")
	}
	switch strings.ToLower(f[0]) {
	case "hari", "minggu", "bulan":
		return s.report(ctx, strings.ToLower(f[0]))
	case "alias":
		return s.alias(ctx, f[1:])
	case "blok":
		return s.block(ctx, f[1:])
	case "buka":
		return s.unblock(ctx, f[1:])
	case "hapus":
		return s.forget(ctx, f[1:])
	}
	return usage, nil
}

func (s *Service) report(ctx context.Context, word string) (string, error) {
	from, to, label, _ := s.Range(word)
	ts, err := s.Totals(ctx, from, to)
	if err != nil {
		return "", err
	}
	if len(ts) == 0 {
		return "Belum ada waktu proyek untuk " + label + ". Nyalakan \"Catat proyek editor\" di menu tray laptop dan atur judul editor (lihat DEPLOY).", nil
	}
	var total int
	for _, t := range ts {
		total += t.Seconds
	}
	var b strings.Builder
	fmt.Fprintf(&b, "💻 Proyek %s — %s", label, Compact(time.Duration(total)*time.Second))
	for _, t := range ts {
		b.WriteString("\n• " + Line(t))
	}
	return b.String(), nil
}

// splitArgs splits on spaces; a "quoted name" stays together.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quoted, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, has = !quoted, true
		case r == ' ' && !quoted:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

func (s *Service) alias(ctx context.Context, f []string) (string, error) {
	if len(f) == 0 {
		as, err := s.DB.ProjectAliases(ctx)
		if err != nil {
			return "", err
		}
		if len(as) == 0 {
			return "Belum ada alias. Pakai: /proyek alias <nama> <alias> (alias yang sama menggabungkan proyek).", nil
		}
		var names []string
		for p, a := range as {
			names = append(names, fmt.Sprintf("• %s → %s", p, a))
		}
		sort.Strings(names)
		return "Alias proyek:\n" + strings.Join(names, "\n"), nil
	}
	name, ok := projects.Clean(f[0])
	if !ok {
		return usage, nil
	}
	if len(f) == 1 { // no alias: remove it
		if err := s.DB.SetProjectAlias(ctx, name, ""); err != nil {
			return "", err
		}
		return fmt.Sprintf("✅ Alias %s dihapus.", name), nil
	}
	alias, ok := projects.Clean(strings.Join(f[1:], " "))
	if !ok {
		return usage, nil
	}
	if err := s.DB.SetProjectAlias(ctx, name, alias); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ %s sekarang tampil sebagai %s (juga untuk hari-hari lalu).", name, alias), nil
}

func (s *Service) block(ctx context.Context, f []string) (string, error) {
	if len(f) == 0 {
		bl, err := s.DB.ProjectBlocklist(ctx)
		if err != nil {
			return "", err
		}
		if len(bl) == 0 {
			return "Tidak ada proyek yang diblok. Pakai: /proyek blok <nama>", nil
		}
		return "Proyek yang tidak dicatat: " + strings.Join(bl, ", "), nil
	}
	name, ok := projects.Clean(strings.Join(f, " "))
	if !ok {
		return usage, nil
	}
	n, err := s.DB.BlockProject(ctx, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ %s tidak akan dicatat lagi (%d catatan lama dihapus). Laptop mengikutinya dalam ≤ 2 menit.", name, n), nil
}

func (s *Service) unblock(ctx context.Context, f []string) (string, error) {
	name, ok := projects.Clean(strings.Join(f, " "))
	if !ok {
		return usage, nil
	}
	if err := s.DB.UnblockProject(ctx, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ %s boleh dicatat lagi.", name), nil
}

func (s *Service) forget(ctx context.Context, f []string) (string, error) {
	name, ok := projects.Clean(strings.Join(f, " "))
	if !ok {
		return usage, nil
	}
	n, err := s.DB.DeleteProjectHistory(ctx, name)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("✅ Riwayat %s dihapus (%d catatan).", name, n), nil
}
