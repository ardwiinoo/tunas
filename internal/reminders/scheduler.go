package reminders

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ardwiinoo/tunas/internal/store"
)

// Notifier sends a text message to the owner.
type Notifier interface {
	NotifyOwner(ctx context.Context, text string) error
}

// Scheduler delivers due reminders.
type Scheduler struct {
	DB          *store.DB
	Notify      Notifier
	Now         func() time.Time
	Loc         *time.Location
	Log         *slog.Logger
	Interval    time.Duration // check period (30s)
	RetryEvery  time.Duration // retry delay after a failed send (1m)
	MaxAttempts int           // attempts before 'failed' (5)
	LateAfter   time.Duration // mark message as late beyond this (5m)
}

// NewScheduler returns a scheduler with PRD defaults.
func NewScheduler(db *store.DB, n Notifier, now func() time.Time, loc *time.Location, log *slog.Logger) *Scheduler {
	return &Scheduler{
		DB: db, Notify: n, Now: now, Loc: loc, Log: log,
		Interval: 30 * time.Second, RetryEvery: time.Minute, MaxAttempts: 5, LateAfter: 5 * time.Minute,
	}
}

// Run ticks until ctx is cancelled. A tick in progress is allowed to finish so
// a reminder is never sent without being marked sent.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if _, err := s.Tick(context.WithoutCancel(ctx)); err != nil {
			s.Log.Error("reminder tick failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick processes all currently due reminders once.
func (s *Scheduler) Tick(ctx context.Context) (int, error) {
	now := s.Now()
	return s.DB.ProcessDue(ctx, now, s.RetryEvery, s.MaxAttempts, 100,
		func(ctx context.Context, r store.Reminder) store.DeliveryResult {
			sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			if err := s.Notify.NotifyOwner(sendCtx, s.message(r, now)); err != nil {
				s.Log.Warn("reminder send failed", "reminder_id", r.ID, "attempt", r.Attempts+1, "err", err)
				return store.DeliveryResult{}
			}
			s.Log.Info("reminder sent", "reminder_id", r.ID, "late_seconds", int(now.Sub(r.RemindAt).Seconds()))
			res := store.DeliveryResult{Sent: true}
			if next, ok := Next(r.Recurrence, r.RemindAt, now, s.Loc); ok {
				res.Next = &next
			}
			return res
		},
		func(r store.Reminder) {
			s.Log.Error("reminder failed permanently", "reminder_id", r.ID)
			msg := fmt.Sprintf("⚠️ Pengingat #%d gagal dikirim %d kali: %s", r.ID, r.Attempts, r.Text)
			if err := s.Notify.NotifyOwner(ctx, msg); err != nil {
				s.Log.Warn("failure notice not sent", "reminder_id", r.ID, "err", err)
			}
		})
}

func (s *Scheduler) message(r store.Reminder, now time.Time) string {
	msg := "⏰ " + r.Text
	if late := now.Sub(r.RemindAt); late > s.LateAfter {
		msg += fmt.Sprintf("\n(terlambat %s, jadwal %s)", humanDuration(late), FormatTime(r.RemindAt, now, s.Loc))
	}
	if d := DescribeRecurrence(r.Recurrence); d != "" {
		msg += "\n🔁 " + d
	}
	return msg
}

// humanDuration renders e.g. "1 j 5 m" / "12 m".
func humanDuration(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%d m", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d j", m/60)
	}
	return fmt.Sprintf("%d j %d m", m/60, m%60)
}
