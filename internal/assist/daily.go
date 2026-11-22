package assist

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ardwiinoo/tunas/internal/activity"
	"github.com/ardwiinoo/tunas/internal/reminders"
)

// Notifier sends a message to the owner.
type Notifier interface {
	NotifyOwner(ctx context.Context, text string) error
}

// DefaultSummaryTime is the default daily summary time (WIB).
const DefaultSummaryTime = "21:00"

// summaryClock returns the configured HH:MM (default 21:00).
func (a *Assistant) summaryClock(ctx context.Context) (int, int) {
	v := DefaultSummaryTime
	var s string
	if ok, err := a.DB.GetSetting(ctx, SummaryTimeKey, &s); err == nil && ok {
		v = s
	}
	var h, m int
	if _, err := fmt.Sscanf(v, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 21, 0
	}
	return h, m
}

// DailyTick sends today's summary once when the configured time has passed.
// Days without data are skipped (marked done, nothing sent). Returns true if sent.
func (a *Assistant) DailyTick(ctx context.Context, n Notifier) (bool, error) {
	now := a.Now().In(a.Loc)
	today := now.Format("2006-01-02")
	var last string
	if _, err := a.DB.GetSetting(ctx, SummaryLastKey, &last); err != nil {
		return false, err
	}
	if last == today {
		return false, nil
	}
	h, m := a.summaryClock(ctx)
	if now.Before(time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, a.Loc)) {
		return false, nil
	}
	text, ok, err := a.DailySummary(ctx)
	if err != nil {
		return false, err
	}
	if ok {
		if err := n.NotifyOwner(ctx, text); err != nil {
			return false, err // retry next tick
		}
	}
	return ok, a.DB.SetSetting(ctx, SummaryLastKey, today)
}

// DailySummary builds today's summary. ok=false if there is no data.
func (a *Assistant) DailySummary(ctx context.Context) (string, bool, error) {
	now := a.Now()
	from, to := activity.DayRange(now, a.Loc)
	bs, err := a.DB.BucketsBetween(ctx, from, to)
	if err != nil {
		return "", false, err
	}
	s := activity.Summarize(bs)
	if s.TotalActive == 0 {
		return "", false, nil
	}
	title := "🌙 Ringkasan " + reminders.FormatDate(now, now, a.Loc)
	facts := activity.FormatToday(s, title, a.Loc)
	text := a.phrase(ctx, a.ActiveForm(ctx),
		"Tulis ringkasan harian yang hangat dan singkat (maks 4 kalimat) dari FAKTA aktivitas laptop hari ini. Sebut total waktu aktif dan aplikasi teratas.",
		facts)
	if text == facts {
		return facts, true, nil
	}
	return title + "\n" + strings.TrimSpace(text), true, nil
}

// RunDaily checks every minute until ctx is done.
func (a *Assistant) RunDaily(ctx context.Context, n Notifier) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if sent, err := a.DailyTick(ctx, n); err != nil {
			a.Log.Warn("daily summary failed", "err", err)
		} else if sent {
			a.Log.Info("daily summary sent")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
