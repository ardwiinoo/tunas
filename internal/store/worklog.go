package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// WorklogEntry is one bullet of the daily work log.
type WorklogEntry struct {
	ID        int64
	Date      time.Time // the local date, as midnight UTC
	Position  int
	Text      string
	Source    string // telegram | agent | import
	CreatedAt time.Time
	UpdatedAt time.Time
}

const worklogCols = `id, entry_date, position, text, source, created_at, updated_at`

func scanWorklog(r pgx.Row, e *WorklogEntry) error {
	return r.Scan(&e.ID, &e.Date, &e.Position, &e.Text, &e.Source, &e.CreatedAt, &e.UpdatedAt)
}

func collectWorklog(rows pgx.Rows, err error) ([]WorklogEntry, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorklogEntry, error) {
		var e WorklogEntry
		return e, scanWorklog(r, &e)
	})
}

// markDirty flags the month of d for the GitHub backup.
func markDirty(ctx context.Context, tx pgx.Tx, d time.Time) error {
	_, err := tx.Exec(ctx, `insert into worklog_sync(month, dirty, changed_at) values ($1, true, clock_timestamp())
		on conflict (month) do update set dirty = true, changed_at = clock_timestamp()`, d.Format("2006-01"))
	return err
}

// AddWorklog appends an entry to the end of its day and returns it with the
// number of entries that day now has.
func (db *DB) AddWorklog(ctx context.Context, date time.Time, text, source string) (WorklogEntry, int, error) {
	var e WorklogEntry
	n := 0
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		// one writer per day at a time keeps positions unique
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('worklog:' || $1::text))`, date.Format(time.DateOnly)); err != nil {
			return err
		}
		if err := scanWorklog(tx.QueryRow(ctx, `insert into worklog_entries(entry_date, position, text, source)
			values ($1, (select coalesce(max(position), -1) + 1 from worklog_entries where entry_date = $1), $2, $3)
			returning `+worklogCols, date, text, source), &e); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from worklog_entries where entry_date = $1`, date).Scan(&n); err != nil {
			return err
		}
		return markDirty(ctx, tx, date)
	})
	return e, n, err
}

// AddWorklogAt is AddWorklog with an explicit created_at (import keeps the
// file's day order, see WorklogMonth).
func (db *DB) AddWorklogAt(ctx context.Context, date time.Time, text, source string, created time.Time) (WorklogEntry, error) {
	var e WorklogEntry
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if err := scanWorklog(tx.QueryRow(ctx, `insert into worklog_entries(entry_date, position, text, source, created_at, updated_at)
			values ($1, (select coalesce(max(position), -1) + 1 from worklog_entries where entry_date = $1), $2, $3, $4, $4)
			returning `+worklogCols, date, text, source, created), &e); err != nil {
			return err
		}
		return markDirty(ctx, tx, date)
	})
	return e, err
}

// WorklogEntry returns one entry.
func (db *DB) WorklogEntry(ctx context.Context, id int64) (WorklogEntry, error) {
	var e WorklogEntry
	err := scanWorklog(db.Pool.QueryRow(ctx, `select `+worklogCols+` from worklog_entries where id = $1`, id), &e)
	return e, notFound(err)
}

// WorklogByDate lists a day's entries in bullet order.
func (db *DB) WorklogByDate(ctx context.Context, date time.Time) ([]WorklogEntry, error) {
	return collectWorklog(db.Pool.Query(ctx, `select `+worklogCols+` from worklog_entries
		where entry_date = $1 order by position, id`, date))
}

// WorklogMonth lists a month's entries in file order: days in the order
// they were first written (oldest entry first, as the old app appends new
// days at the end), bullets in position order.
func (db *DB) WorklogMonth(ctx context.Context, month time.Time) ([]WorklogEntry, error) {
	from := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	return collectWorklog(db.Pool.Query(ctx, `select e.id, e.entry_date, e.position, e.text, e.source, e.created_at, e.updated_at
		from worklog_entries e
		join (select entry_date, min(created_at) as first from worklog_entries
		      where entry_date >= $1 and entry_date < $2 group by entry_date) f using (entry_date)
		order by f.first, e.entry_date, e.position, e.id`, from, from.AddDate(0, 1, 0)))
}

// WorklogCounts returns entries per day ("YYYY-MM-DD") in [from, to).
func (db *DB) WorklogCounts(ctx context.Context, from, to time.Time) (map[string]int, error) {
	rows, err := db.Pool.Query(ctx, `select entry_date, count(*) from worklog_entries
		where entry_date >= $1 and entry_date < $2 group by entry_date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var d time.Time
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		out[d.Format(time.DateOnly)] = n
	}
	return out, rows.Err()
}

// WorklogExists reports whether the exact text is already on that date
// (import is idempotent on date + text).
func (db *DB) WorklogExists(ctx context.Context, date time.Time, text string) (bool, error) {
	var ok bool
	err := db.Pool.QueryRow(ctx, `select exists(select 1 from worklog_entries where entry_date = $1 and text = $2)`, date, text).Scan(&ok)
	return ok, err
}

// UpdateWorklog replaces an entry's text.
func (db *DB) UpdateWorklog(ctx context.Context, id int64, text string, now time.Time) (WorklogEntry, error) {
	var e WorklogEntry
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if err := scanWorklog(tx.QueryRow(ctx, `update worklog_entries set text = $2, updated_at = $3 where id = $1
			returning `+worklogCols, id, text, now), &e); err != nil {
			return notFound(err)
		}
		return markDirty(ctx, tx, e.Date)
	})
	return e, err
}

// DeleteWorklog removes an entry and returns it.
func (db *DB) DeleteWorklog(ctx context.Context, id int64) (WorklogEntry, error) {
	var e WorklogEntry
	err := pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if err := scanWorklog(tx.QueryRow(ctx, `delete from worklog_entries where id = $1 returning `+worklogCols, id), &e); err != nil {
			return notFound(err)
		}
		return markDirty(ctx, tx, e.Date)
	})
	return e, err
}

// LastWorklogAt is the newest entry's creation time on date (zero if none).
func (db *DB) LastWorklogAt(ctx context.Context, date time.Time) (time.Time, int, error) {
	var last *time.Time
	var n int
	err := db.Pool.QueryRow(ctx, `select max(created_at), count(*) from worklog_entries where entry_date = $1`, date).Scan(&last, &n)
	if last == nil {
		return time.Time{}, n, err
	}
	return *last, n, err
}

// --- calendar ---------------------------------------------------------------

// Holiday is a row of the holidays table.
type Holiday struct {
	Day  time.Time
	Name string
	Type string // holiday | leave
}

// HolidaysBetween lists holidays in [from, to).
func (db *DB) HolidaysBetween(ctx context.Context, from, to time.Time) ([]Holiday, error) {
	rows, err := db.Pool.Query(ctx, `select day, name, type from holidays where day >= $1 and day < $2 order by day, name`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Holiday, error) {
		var h Holiday
		return h, r.Scan(&h.Day, &h.Name, &h.Type)
	})
}

// UpsertHolidays stores fetched holidays.
func (db *DB) UpsertHolidays(ctx context.Context, hs []Holiday, now time.Time) error {
	return pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		for _, h := range hs {
			if _, err := tx.Exec(ctx, `insert into holidays(day, name, type, fetched_at) values ($1, $2, $3, $4)
				on conflict (day, name) do update set type = excluded.type, fetched_at = excluded.fetched_at`,
				h.Day, h.Name, h.Type, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// HolidaysFetchedAt is the newest fetch time for a year (zero if none).
func (db *DB) HolidaysFetchedAt(ctx context.Context, year int) (time.Time, error) {
	var t *time.Time
	err := db.Pool.QueryRow(ctx, `select max(fetched_at) from holidays where extract(year from day) = $1`, year).Scan(&t)
	if t == nil {
		return time.Time{}, err
	}
	return *t, err
}

// Overrides lists /libur and /kerja marks in [from, to) as day → kind.
func (db *DB) Overrides(ctx context.Context, from, to time.Time) (map[string]string, error) {
	rows, err := db.Pool.Query(ctx, `select day, kind from calendar_overrides where day >= $1 and day < $2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var d time.Time
		var k string
		if err := rows.Scan(&d, &k); err != nil {
			return nil, err
		}
		out[d.Format(time.DateOnly)] = k
	}
	return out, rows.Err()
}

// SetOverride marks a day off or work (the owner's correction).
func (db *DB) SetOverride(ctx context.Context, day time.Time, kind, note string) error {
	_, err := db.Pool.Exec(ctx, `insert into calendar_overrides(day, kind, note) values ($1, $2, nullif($3, ''))
		on conflict (day) do update set kind = excluded.kind, note = excluded.note`, day, kind, note)
	return err
}

// DeleteOverride removes a mark; false if there was none.
func (db *DB) DeleteOverride(ctx context.Context, day time.Time) (bool, error) {
	tag, err := db.Pool.Exec(ctx, `delete from calendar_overrides where day = $1`, day)
	return tag.RowsAffected() > 0, err
}

// --- pending text replies ---------------------------------------------------

// SetPending waits for the chat's next text message (replaces any wait).
func (db *DB) SetPending(ctx context.Context, chatID int64, kind string, payload any, expires time.Time) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `insert into bot_pending(chat_id, kind, payload, expires_at) values ($1, $2, $3, $4)
		on conflict (chat_id) do update set kind = excluded.kind, payload = excluded.payload, expires_at = excluded.expires_at`,
		chatID, kind, raw, expires)
	return err
}

// TakePending removes and returns the chat's wait if it has not expired.
func (db *DB) TakePending(ctx context.Context, chatID int64, now time.Time) (kind string, payload []byte, ok bool, err error) {
	var exp time.Time
	err = db.Pool.QueryRow(ctx, `delete from bot_pending where chat_id = $1 returning kind, payload, expires_at`, chatID).
		Scan(&kind, &payload, &exp)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil || !now.Before(exp) {
		return "", nil, false, err
	}
	return kind, payload, true, nil
}

// ClearPending drops the chat's wait.
func (db *DB) ClearPending(ctx context.Context, chatID int64) error {
	_, err := db.Pool.Exec(ctx, `delete from bot_pending where chat_id = $1`, chatID)
	return err
}

// --- reminder prompts -------------------------------------------------------

// WorklogPrompt is a row of worklog_prompts.
type WorklogPrompt struct {
	ID          int64
	Date        time.Time
	Slot        string // "10:30" | ... | "closing"
	MessageID   *int64
	Status      string // sent | answered | later | skipped | expired
	SentAt      time.Time
	AnsweredVia *string
}

const promptCols = `id, slot_date, slot, telegram_message_id, status, sent_at, answered_via`

func scanPrompt(r pgx.Row, p *WorklogPrompt) error {
	return r.Scan(&p.ID, &p.Date, &p.Slot, &p.MessageID, &p.Status, &p.SentAt, &p.AnsweredVia)
}

// ClaimPrompt records a slot exactly once (the unique key is the
// idempotency guard). inserted=false: the slot was already handled.
func (db *DB) ClaimPrompt(ctx context.Context, date time.Time, slot, status string, now time.Time) (WorklogPrompt, bool, error) {
	var p WorklogPrompt
	err := scanPrompt(db.Pool.QueryRow(ctx, `insert into worklog_prompts(slot_date, slot, status, sent_at)
		values ($1, $2, $3, $4) on conflict (slot_date, slot) do nothing returning `+promptCols, date, slot, status, now), &p)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	return p, err == nil, err
}

// SetPromptMessage stores the Telegram message of a prompt.
func (db *DB) SetPromptMessage(ctx context.Context, id int64, msgID int) error {
	_, err := db.Pool.Exec(ctx, `update worklog_prompts set telegram_message_id = $2 where id = $1`, id, msgID)
	return err
}

// PromptsOn lists the prompts of a day in slot order of sending.
func (db *DB) PromptsOn(ctx context.Context, date time.Time) ([]WorklogPrompt, error) {
	rows, err := db.Pool.Query(ctx, `select `+promptCols+` from worklog_prompts where slot_date = $1 order by sent_at, id`, date)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorklogPrompt, error) {
		var p WorklogPrompt
		return p, scanPrompt(r, &p)
	})
}

// Prompt returns one prompt.
func (db *DB) Prompt(ctx context.Context, id int64) (WorklogPrompt, error) {
	var p WorklogPrompt
	err := scanPrompt(db.Pool.QueryRow(ctx, `select `+promptCols+` from worklog_prompts where id = $1`, id), &p)
	return p, notFound(err)
}

// AnswerPrompt moves an open prompt (sent or later) to status. ErrNotFound
// when it is not open any more (answered elsewhere, skipped, expired).
func (db *DB) AnswerPrompt(ctx context.Context, id int64, status, via string, now time.Time) (WorklogPrompt, error) {
	var p WorklogPrompt
	err := scanPrompt(db.Pool.QueryRow(ctx, `update worklog_prompts set status = $2, answered_via = $3, answered_at = $4
		where id = $1 and status in ('sent', 'later') returning `+promptCols, id, status, via, now), &p)
	return p, notFound(err)
}

// ClearPromptMessage forgets a prompt's message after it was deleted.
func (db *DB) ClearPromptMessage(ctx context.Context, id int64) error {
	_, err := db.Pool.Exec(ctx, `update worklog_prompts set telegram_message_id = null where id = $1`, id)
	return err
}

// --- GitHub backup state ----------------------------------------------------

// WorklogSync is a row of worklog_sync.
type WorklogSync struct {
	Month           string // "2026-11"
	Dirty           bool
	LastSHA         *string
	LastContentHash *string
	LastSyncedAt    *time.Time
	LastError       *string
	Status          string // pending | ok | conflict | error
	ChangedAt       time.Time
}

const syncCols = `month, dirty, last_sha, last_content_hash, last_synced_at, last_error, status, changed_at`

func scanSync(r pgx.Row, s *WorklogSync) error {
	return r.Scan(&s.Month, &s.Dirty, &s.LastSHA, &s.LastContentHash, &s.LastSyncedAt, &s.LastError, &s.Status, &s.ChangedAt)
}

// SyncStates lists the backup state of every month (oldest first).
func (db *DB) SyncStates(ctx context.Context) ([]WorklogSync, error) {
	rows, err := db.Pool.Query(ctx, `select `+syncCols+` from worklog_sync order by month`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorklogSync, error) {
		var s WorklogSync
		return s, scanSync(r, &s)
	})
}

// SyncState returns one month's state (ErrNotFound if it was never touched).
func (db *DB) SyncState(ctx context.Context, month string) (WorklogSync, error) {
	var s WorklogSync
	err := scanSync(db.Pool.QueryRow(ctx, `select `+syncCols+` from worklog_sync where month = $1`, month), &s)
	return s, notFound(err)
}

// MarkMonthDirty flags a month for backup (e.g. /worklog sync).
func (db *DB) MarkMonthDirty(ctx context.Context, month string) error {
	_, err := db.Pool.Exec(ctx, `insert into worklog_sync(month, dirty, changed_at) values ($1, true, '-infinity')
		on conflict (month) do update set dirty = true`, month)
	return err
}

// SyncOK records a successful push. The month stays dirty when it changed
// again after changedAt (the change was not in the pushed content).
func (db *DB) SyncOK(ctx context.Context, month, sha, hash string, changedAt, now time.Time) error {
	_, err := db.Pool.Exec(ctx, `update worklog_sync set last_sha = $2, last_content_hash = $3, last_synced_at = $5,
		last_error = null, status = 'ok', dirty = (changed_at > $4) where month = $1`, month, sha, hash, changedAt, now)
	return err
}

// SyncFailed records a failed push (status error or conflict).
func (db *DB) SyncFailed(ctx context.Context, month, status, msg string) error {
	_, err := db.Pool.Exec(ctx, `update worklog_sync set status = $2, last_error = $3 where month = $1`, month, status, msg)
	return err
}

// SyncImported records a month that matches a file already in the repo.
func (db *DB) SyncImported(ctx context.Context, month, sha, hash string, dirty bool, now time.Time) error {
	_, err := db.Pool.Exec(ctx, `insert into worklog_sync(month, dirty, last_sha, last_content_hash, last_synced_at, status, changed_at)
		values ($1, $4, $2, $3, $5, 'ok', $5)
		on conflict (month) do update set dirty = $4, last_sha = $2, last_content_hash = $3, last_synced_at = $5,
		status = 'ok', last_error = null, changed_at = $5`, month, sha, hash, dirty, now)
	return err
}

// AddWorklogFromAgent saves an entry sent by the agent. clientID makes it
// idempotent: a resend returns the stored entry with duplicate=true.
func (db *DB) AddWorklogFromAgent(ctx context.Context, date time.Time, text, clientID string) (e WorklogEntry, count int, duplicate bool, err error) {
	err = pgx.BeginFunc(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('worklog:' || $1::text))`, date.Format(time.DateOnly)); err != nil {
			return err
		}
		err := scanWorklog(tx.QueryRow(ctx, `insert into worklog_entries(entry_date, position, text, source, client_id)
			values ($1, (select coalesce(max(position), -1) + 1 from worklog_entries where entry_date = $1), $2, 'agent', $3)
			on conflict (client_id) do nothing returning `+worklogCols, date, text, clientID), &e)
		if errors.Is(err, pgx.ErrNoRows) {
			duplicate = true
			err = scanWorklog(tx.QueryRow(ctx, `select `+worklogCols+` from worklog_entries where client_id = $1`, clientID), &e)
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) from worklog_entries where entry_date = $1`, e.Date).Scan(&count); err != nil {
			return err
		}
		if duplicate {
			return nil
		}
		return markDirty(ctx, tx, date)
	})
	return e, count, duplicate, err
}
