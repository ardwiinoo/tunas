package worklog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/bot"
	"github.com/ardwiinoo/tunas/internal/store"
)

// Settings keys (PRD 5.1).
const (
	KeyEnabled   = "worklog.enabled"
	KeyTimes     = "worklog.reminder_times"
	KeyClosing   = "worklog.closing_time"
	KeyMinGap    = "worklog.min_gap_minutes"
	KeyMaxPerDay = "worklog.max_prompts_per_day"
)

// SlotClosing is the closing reminder after a postponed last slot.
const SlotClosing = "closing"

// CatchUp is how late a slot may still be sent (after a restart).
const CatchUp = 30 * time.Minute

// ReminderSettings are the worklog reminder settings.
type ReminderSettings struct {
	Enabled   bool
	Times     []string // "HH:MM", ascending
	Closing   string
	MinGap    time.Duration
	MaxPerDay int
}

// DefaultSettings are PRD 5.1's defaults.
func DefaultSettings() ReminderSettings {
	return ReminderSettings{Enabled: true, Times: []string{"10:30", "13:30", "16:30"}, Closing: "17:30",
		MinGap: 90 * time.Minute, MaxPerDay: 4}
}

// Messenger is the Telegram side (bot.Telegram implements it).
type Messenger interface {
	SendReplyID(ctx context.Context, chatID int64, r bot.Reply) (int, error)
	EditReply(ctx context.Context, chatID int64, msgID int, r bot.Reply) error
	DeleteMessage(ctx context.Context, chatID int64, msgID int) error
}

// Reminder sends the worklog prompts (PRD 6.5).
type Reminder struct {
	Svc *Service
	TG  Messenger
}

// LoadSettings reads the settings with defaults.
func (r *Reminder) LoadSettings(ctx context.Context) (ReminderSettings, error) {
	st := DefaultSettings()
	db := r.Svc.DB
	if _, err := db.GetSetting(ctx, KeyEnabled, &st.Enabled); err != nil {
		return st, err
	}
	if _, err := db.GetSetting(ctx, KeyTimes, &st.Times); err != nil {
		return st, err
	}
	if _, err := db.GetSetting(ctx, KeyClosing, &st.Closing); err != nil {
		return st, err
	}
	gap := int(st.MinGap / time.Minute)
	if _, err := db.GetSetting(ctx, KeyMinGap, &gap); err != nil {
		return st, err
	}
	st.MinGap = time.Duration(gap) * time.Minute
	if _, err := db.GetSetting(ctx, KeyMaxPerDay, &st.MaxPerDay); err != nil {
		return st, err
	}
	sort.Strings(st.Times)
	return st, nil
}

// ParseClock reads "HH:MM".
func ParseClock(s string) (h, m int, err error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, fmt.Errorf("jam %q tidak valid (pakai HH:MM)", s)
	}
	return t.Hour(), t.Minute(), nil
}

func (r *Reminder) at(day time.Time, clock string) (time.Time, error) {
	h, m, err := ParseClock(clock)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, r.Svc.Loc), nil
}

func open(p store.WorklogPrompt) bool { return p.Status == "sent" || p.Status == "later" }

// Tick checks the slots of today (call every 30 s).
func (r *Reminder) Tick(ctx context.Context) error {
	s := r.Svc
	now := s.Now()
	day := LocalDate(now, s.Loc)
	st, err := r.LoadSettings(ctx)
	if err != nil || !st.Enabled {
		return err
	}
	rules, err := s.Rules(ctx, day, day.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	if !rules.IsWorkday(day) {
		return nil
	}
	prompts, err := s.DB.PromptsOn(ctx, day)
	if err != nil {
		return err
	}
	handled := map[string]store.WorklogPrompt{}
	skipped, sent := false, 0
	for _, p := range prompts {
		handled[p.Slot] = p
		skipped = skipped || p.Status == "skipped"
		if wasSent(p) {
			sent++
		}
	}
	slots := append([]string(nil), st.Times...)
	if last, ok := handled[lastSlot(st)]; ok && last.Status == "later" {
		slots = append(slots, SlotClosing) // only after the last slot was postponed
	}
	for _, slot := range slots {
		if _, done := handled[slot]; done {
			continue
		}
		clock := slot
		if slot == SlotClosing {
			clock = st.Closing
		}
		due, err := r.at(day, clock)
		if err != nil {
			return err
		}
		if now.Before(due) {
			continue
		}
		lastAt, count, err := s.DB.LastWorklogAt(ctx, day)
		if err != nil {
			return err
		}
		reason := ""
		switch {
		case now.Sub(due) > CatchUp:
			reason = "late"
		case skipped:
			reason = "skipped today"
		case !lastAt.IsZero() && now.Sub(lastAt) < st.MinGap:
			reason = "recent entry"
		case sent >= st.MaxPerDay:
			reason = "daily maximum"
		}
		if reason != "" {
			if _, _, err := s.DB.ClaimPrompt(ctx, day, slot, "expired", now); err != nil {
				return err
			}
			s.Log.Info("worklog slot not sent", "slot", slot, "reason", reason)
			continue
		}
		if err := r.send(ctx, day, slot, clock, count, lastAt, prompts, st); err != nil {
			return err
		}
		sent++
	}
	return nil
}

// wasSent: the prompt reached Telegram (a superseded one still counts
// towards the daily maximum).
func wasSent(p store.WorklogPrompt) bool {
	return p.Status != "expired" || (p.AnsweredVia != nil && *p.AnsweredVia == "superseded")
}

func lastSlot(st ReminderSettings) string {
	if len(st.Times) == 0 {
		return ""
	}
	return st.Times[len(st.Times)-1]
}

func (r *Reminder) send(ctx context.Context, day time.Time, slot, clock string, count int, lastAt time.Time,
	earlier []store.WorklogPrompt, st ReminderSettings) error {
	s := r.Svc
	p, inserted, err := s.DB.ClaimPrompt(ctx, day, slot, "sent", s.Now())
	if err != nil || !inserted {
		return err // another instance (or an earlier tick) has it
	}
	// keep the chat tidy: the previous prompt message goes away
	for _, old := range earlier {
		if old.MessageID != nil && open(old) {
			if err := r.TG.DeleteMessage(ctx, s.ChatID, int(*old.MessageID)); err != nil {
				s.Log.Debug("old prompt not deleted", "err", err)
			}
			if err := s.DB.ClearPromptMessage(ctx, old.ID); err != nil {
				return err
			}
			if old.Status == "sent" {
				_, _ = s.DB.AnswerPrompt(ctx, old.ID, "expired", "superseded", s.Now())
			}
		}
	}
	msgID, err := r.TG.SendReplyID(ctx, s.ChatID, PromptReply(p.ID, day, clock, count, lastAt.In(s.Loc), slot == SlotClosing))
	if err != nil {
		s.Log.Warn("worklog prompt not sent", "slot", slot, "err", err) // claimed: never sent twice
	} else if err := s.DB.SetPromptMessage(ctx, p.ID, msgID); err != nil {
		return err
	}
	s.Log.Info("worklog prompt sent", "slot", slot, "prompt", p.ID)
	return s.DB.CreateEvent(ctx, "worklog.due", map[string]any{"prompt_id": p.ID, "slot": slot, "count_today": count})
}

// PromptReply is the reminder message (PRD 6.5); the closing reminder has
// no "Nanti".
func PromptReply(id int64, day time.Time, clock string, count int, lastAt time.Time, closing bool) bot.Reply {
	var b strings.Builder
	fmt.Fprintf(&b, "📝 Worklog · %s %d %s · %s\n", dayNames[day.Weekday()], day.Day(), monthNames[day.Month()-1][:3], clock)
	if count == 0 {
		b.WriteString("Hari ini belum ada catatan.\n")
	} else {
		fmt.Fprintf(&b, "Hari ini baru ada %d catatan (%s).\n", count, lastAt.Format("15:04"))
	}
	b.WriteString("Lagi ngerjain apa?")
	write := bot.Button{Text: "✍️ Tulis sekarang", Data: fmt.Sprintf("wlw:%d", id)}
	skip := bot.Button{Text: "🙈 Lewati hari ini", Data: fmt.Sprintf("wls:%d", id)}
	if closing {
		return bot.Reply{Text: b.String(), Rows: [][]bot.Button{{write, skip}}}
	}
	later := bot.Button{Text: "⏭ Nanti", Data: fmt.Sprintf("wln:%d", id)}
	return bot.Reply{Text: b.String(), Rows: [][]bot.Button{{write, later, skip}}}
}

// Callback handles the prompt buttons wlw/wln/wls.
func (r *Reminder) Callback(ctx context.Context, data string) (bot.Reply, bool) {
	verb, arg, _ := strings.Cut(data, ":")
	if verb != "wlw" && verb != "wln" && verb != "wls" && verb != "wlc" {
		return bot.Reply{}, false
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return bot.Reply{Keep: true}, true
	}
	s := r.Svc
	p, err := s.DB.Prompt(ctx, id)
	if err != nil || !open(p) {
		return bot.Reply{Text: "Pengingat ini sudah selesai."}, true
	}
	switch verb {
	case "wlw":
		if err := s.DB.SetPending(ctx, s.ChatID, PendingWrite, pendingPayload{PromptID: id}, s.Now().Add(pendingTTL)); err != nil {
			return errReply(s, "pending", err), true
		}
		return bot.Reply{Text: "✍️ Kirim catatanmu sekarang (satu pesan = satu catatan).",
			Rows: [][]bot.Button{{{Text: "❌ Batal", Data: fmt.Sprintf("wlc:%d", id)}}}}, true
	case "wlc": // cancel writing: the reminder comes back with its buttons
		if err := s.DB.ClearPending(ctx, s.ChatID); err != nil {
			return errReply(s, "pending", err), true
		}
		day := LocalDate(p.SentAt, s.Loc)
		lastAt, count, err := s.DB.LastWorklogAt(ctx, day)
		if err != nil {
			return errReply(s, "prompt", err), true
		}
		clock := p.Slot
		if p.Slot == SlotClosing {
			if st, err := r.LoadSettings(ctx); err == nil {
				clock = st.Closing
			}
		}
		return PromptReply(id, day, clock, count, lastAt.In(s.Loc), p.Slot == SlotClosing), true
	case "wln":
		text, err := r.Answer(ctx, id, "later", "telegram")
		if err != nil {
			return errReply(s, "later", err), true
		}
		return bot.Reply{Text: text}, true
	default:
		text, err := r.Answer(ctx, id, "skip", "telegram")
		if err != nil {
			return errReply(s, "skip", err), true
		}
		return bot.Reply{Text: text}, true
	}
}

// ErrPromptClosed means the prompt was already answered, skipped or expired.
var ErrPromptClosed = errors.New("prompt already closed")

// Answer applies "later" or "skip" from Telegram or the agent, tells the
// other side (event or message edit) and returns the new message text.
func (r *Reminder) Answer(ctx context.Context, id int64, action, via string) (string, error) {
	s := r.Svc
	status := map[string]string{"later": "later", "skip": "skipped"}[action]
	if status == "" {
		return "", fmt.Errorf("unknown action %q", action)
	}
	p, err := s.DB.AnswerPrompt(ctx, id, status, via, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrPromptClosed
	}
	if err != nil {
		return "", err
	}
	text := "🙈 Dilewati untuk hari ini."
	if status == "later" {
		text = "⏭ Ditunda ke pengingat berikutnya."
		if st, err := r.LoadSettings(ctx); err == nil && p.Slot == lastSlot(st) {
			text = "⏭ Ditunda. Pengingat penutup jam " + st.Closing + "."
		}
	}
	r.closed(ctx, p, via, text)
	return text, nil
}

// closed tells the side that did not answer: the agent gets worklog.closed,
// and an answer from the agent edits the Telegram message.
func (r *Reminder) closed(ctx context.Context, p store.WorklogPrompt, via, text string) {
	s := r.Svc
	if err := s.DB.CreateEvent(ctx, "worklog.closed", map[string]any{"prompt_id": p.ID, "via": via}); err != nil {
		s.Log.Warn("worklog.closed event failed", "err", err)
	}
	if via != "telegram" && p.MessageID != nil {
		if err := r.TG.EditReply(ctx, s.ChatID, int(*p.MessageID), bot.Reply{Text: text}); err != nil {
			s.Log.Debug("prompt message not edited", "err", err)
		}
	}
}

// OnSaved closes today's open prompts when an entry is saved anywhere.
func (r *Reminder) OnSaved(ctx context.Context, e store.WorklogEntry, via string, _ int64) {
	s := r.Svc
	if !e.Date.Equal(LocalDate(s.Now(), s.Loc)) {
		return
	}
	prompts, err := s.DB.PromptsOn(ctx, e.Date)
	if err != nil {
		s.Log.Warn("prompts not read", "err", err)
		return
	}
	for _, p := range prompts {
		if !open(p) {
			continue
		}
		if _, err := s.DB.AnswerPrompt(ctx, p.ID, "answered", via, s.Now()); err != nil {
			continue
		}
		if p.MessageID != nil {
			if err := r.TG.EditReply(ctx, s.ChatID, int(*p.MessageID), bot.Reply{Text: "✅ Worklog sudah diisi."}); err != nil {
				s.Log.Debug("prompt message not edited", "err", err)
			}
		}
		if err := s.DB.CreateEvent(ctx, "worklog.closed", map[string]any{"prompt_id": p.ID, "via": via}); err != nil {
			s.Log.Warn("worklog.closed event failed", "err", err)
		}
	}
}

// Command handles /worklog on|off|jam|penutup (status without arguments).
func (r *Reminder) Command(ctx context.Context, args string) bot.Reply {
	s := r.Svc
	f := strings.Fields(strings.ToLower(args))
	set := func(key string, v any) bot.Reply {
		if err := s.DB.SetSetting(ctx, key, v); err != nil {
			return errReply(s, "settings", err)
		}
		return r.status(ctx, "✅ Tersimpan.\n")
	}
	switch {
	case len(f) == 0:
		return r.status(ctx, "")
	case f[0] == "on" || f[0] == "off":
		return set(KeyEnabled, f[0] == "on")
	case f[0] == "jam" && len(f) >= 2:
		var times []string
		for _, part := range strings.Split(strings.Join(f[1:], ""), ",") {
			h, m, err := ParseClock(part)
			if err != nil {
				return bot.Reply{Text: "❌ " + err.Error()}
			}
			times = append(times, fmt.Sprintf("%02d:%02d", h, m))
		}
		sort.Strings(times)
		if len(times) > 6 {
			return bot.Reply{Text: "❌ Maksimal 6 jam pengingat."}
		}
		return set(KeyTimes, times)
	case f[0] == "penutup" && len(f) == 2:
		h, m, err := ParseClock(f[1])
		if err != nil {
			return bot.Reply{Text: "❌ " + err.Error()}
		}
		return set(KeyClosing, fmt.Sprintf("%02d:%02d", h, m))
	}
	return bot.Reply{Text: "Pakai: /worklog [on|off] · /worklog jam 10:30,13:30,16:30 · /worklog penutup 17:30"}
}

func (r *Reminder) status(ctx context.Context, prefix string) bot.Reply {
	st, err := r.LoadSettings(ctx)
	if err != nil {
		return errReply(r.Svc, "settings", err)
	}
	state := "nyala"
	if !st.Enabled {
		state = "mati"
	}
	return bot.Reply{Text: fmt.Sprintf("%s📝 Pengingat worklog: %s\nJam: %s · penutup %s (bila slot terakhir ditunda)\nHanya di hari kerja; tidak dikirim bila ada catatan %d menit terakhir; maks %d per hari.",
		prefix, state, strings.Join(st.Times, ", "), st.Closing, int(st.MinGap/time.Minute), st.MaxPerDay)}
}

// Run ticks every 30 s until ctx is done.
func (r *Reminder) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.Svc.Log.Warn("worklog reminder tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
