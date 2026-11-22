package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Reminder is a scheduled notification.
type Reminder struct {
	ID         int64
	TaskID     *int64
	Text       string
	RemindAt   time.Time
	Recurrence string // "" | daily | weekdays | weekly:MON
	Status     string
	Attempts   int
}

const reminderCols = `id, task_id, text, remind_at, coalesce(recurrence, ''), status, attempts`

func scanReminder(r pgx.Row) (Reminder, error) {
	var m Reminder
	return m, r.Scan(&m.ID, &m.TaskID, &m.Text, &m.RemindAt, &m.Recurrence, &m.Status, &m.Attempts)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateReminder inserts a pending reminder.
func (db *DB) CreateReminder(ctx context.Context, text string, at time.Time, recurrence string) (Reminder, error) {
	return scanReminder(db.Pool.QueryRow(ctx,
		`insert into reminders(text, remind_at, recurrence) values($1, $2, $3) returning `+reminderCols,
		text, at, nullIfEmpty(recurrence)))
}

// PendingReminders lists pending reminders by due time.
func (db *DB) PendingReminders(ctx context.Context, limit int) ([]Reminder, error) {
	rows, err := db.Pool.Query(ctx,
		`select `+reminderCols+` from reminders where status='pending' order by remind_at, id limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Reminder, error) { return scanReminder(r) })
}

// CancelReminder cancels a pending reminder. ErrNotFound if none pending with that id.
func (db *DB) CancelReminder(ctx context.Context, id int64) (Reminder, error) {
	m, err := scanReminder(db.Pool.QueryRow(ctx,
		`update reminders set status='cancelled' where id=$1 and status='pending' returning `+reminderCols, id))
	return m, notFound(err)
}

// DeliveryResult tells the store what happened to a claimed reminder.
type DeliveryResult struct {
	Sent bool
	// Next, when non-nil and Sent, schedules the next occurrence of a recurring reminder.
	Next *time.Time
}

// ProcessDue claims due reminders one at a time (FOR UPDATE SKIP LOCKED) and
// calls deliver while the row is locked. The status update commits in the same
// transaction as the claim, so a reminder is marked sent only once.
// A failed reminder is retried after retryEvery; after maxAttempts it becomes
// 'failed'. onFailed is called (outside the tx) for reminders that just failed.
// Returns the number of reminders processed.
func (db *DB) ProcessDue(ctx context.Context, now time.Time, retryEvery time.Duration, maxAttempts, limit int,
	deliver func(ctx context.Context, r Reminder) DeliveryResult,
	onFailed func(r Reminder),
) (int, error) {
	n := 0
	for n < limit {
		var failed *Reminder
		found := false
		err := db.withEvents(ctx, func(tx pgx.Tx) error {
			r, err := scanReminder(tx.QueryRow(ctx,
				`select `+reminderCols+` from reminders
				 where status='pending' and remind_at <= $1
				   and (last_attempt_at is null or last_attempt_at <= $2)
				 order by remind_at, id limit 1 for update skip locked`,
				now, now.Add(-retryEvery)))
			if err != nil {
				return notFound(err)
			}
			found = true
			res := deliver(ctx, r)
			if !res.Sent {
				r.Attempts++
				status := "pending"
				if r.Attempts >= maxAttempts {
					status = "failed"
					failed = &r
				}
				_, err := tx.Exec(ctx,
					`update reminders set attempts=$2, last_attempt_at=$3, status=$4 where id=$1`,
					r.ID, r.Attempts, now, status)
				return err
			}
			if _, err := tx.Exec(ctx,
				`update reminders set status='sent', sent_at=$2, attempts=attempts+1, last_attempt_at=$2 where id=$1`,
				r.ID, now); err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, "reminder.due", map[string]any{
				"reminder_id": r.ID, "text": r.Text, "remind_at": r.RemindAt,
			}); err != nil {
				return err
			}
			if res.Next != nil {
				_, err := tx.Exec(ctx,
					`insert into reminders(task_id, text, remind_at, recurrence) values($1, $2, $3, $4)`,
					r.TaskID, r.Text, *res.Next, nullIfEmpty(r.Recurrence))
				return err
			}
			return nil
		})
		if errors.Is(err, ErrNotFound) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if !found {
			return n, nil
		}
		n++
		if failed != nil && onFailed != nil {
			onFailed(*failed)
		}
	}
	return n, nil
}
