package overlay

import (
	"context"
	"net/url"
	"strings"

	"github.com/ardwiinoo/tunas/internal/agent"
)

// Host is the agent side the tray and the worklog bubble need.
type Host interface {
	// WriteWorklog saves an entry (local queue first, then the server).
	WriteWorklog(ctx context.Context, text string, promptID int64) (agent.WriteResult, error)
	// AnswerPrompt answers a worklog reminder: "later" | "skip".
	AnswerPrompt(ctx context.Context, id int64, action string) error
	// MuteWorklog switches today's worklog reminders off ("Senyapkan hari ini").
	MuteWorklog(ctx context.Context) error
	// Calendar is the source of the worklog calendar (server first, the
	// cache when offline); nil = no calendar.
	Calendar() CalendarSource
	// Command runs a whitelisted slash command on the server.
	Command(ctx context.Context, cmd, args string) (agent.CommandResult, error)
	// StatusReport is the status card: live local numbers plus the server summary.
	StatusReport(ctx context.Context) string
	// Status is the tray's first line, e.g. "Tunas · online · sinkron 12 d lalu".
	Status() string
	// SaveState persists the tray switches (state.json).
	SaveState(st agent.UIState)
}

// CalendarSource is what the calendar card needs.
type CalendarSource interface {
	Month(ctx context.Context, month string) (agent.CalMonth, bool, error)
	Day(ctx context.Context, date string) (agent.CalDayView, bool, error)
	Edit(ctx context.Context, id int64, text string) error
	Delete(ctx context.Context, id int64) error
	// AddOn writes entries for a day (queued while offline).
	AddOn(ctx context.Context, date, text string) (agent.WriteResult, error)
}

// SlashCommand splits "/name args" (name lower-case letters only).
func SlashCommand(s string) (name, args string, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '/' {
		return "", "", false
	}
	head, rest, _ := strings.Cut(s[1:], " ")
	if head == "" || len(head) > 20 {
		return "", "", false
	}
	for _, r := range head {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "", "", false
		}
	}
	return strings.ToLower(head), strings.TrimSpace(rest), true
}

// IsCalCommand reports whether chat input is "/cal" (opens the calendar card).
func IsCalCommand(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "/cal") }

// TrayOptions are set before Run.
type TrayOptions struct {
	Host      Host
	State     agent.UIState  // initial switches from state.json
	Registry  agent.Registry // autostart (nil = item hidden)
	ExePath   string         // for the autostart value
	ConfigDir string         // "Buka folder konfigurasi"
	// OfferAutostart shows the first-run question once (not with --background).
	OfferAutostart bool
}

// SourceNames turns search results into the short site names shown under
// an answer ("kompas.com"), without duplicates, at most three.
func SourceNames(src []agent.ChatSource) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range src {
		h := siteName(s.URL)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
		if len(out) == 3 {
			break
		}
	}
	return out
}

func siteName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
