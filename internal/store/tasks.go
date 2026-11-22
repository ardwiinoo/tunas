package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Task is a to-do item.
type Task struct {
	ID          int64
	Title       string
	Status      string
	CreatedAt   time.Time
	CompletedAt *time.Time
}

// CreateTask inserts an open task and emits a task.created event.
func (db *DB) CreateTask(ctx context.Context, title string) (Task, error) {
	var t Task
	err := db.withEvents(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`insert into tasks(title) values($1) returning id, title, status, created_at`, title,
		).Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt); err != nil {
			return err
		}
		return insertEvent(ctx, tx, "task.created", map[string]any{"task_id": t.ID, "title": t.Title})
	})
	return t, err
}

// OpenTasks lists open tasks, oldest first.
func (db *DB) OpenTasks(ctx context.Context, limit int) ([]Task, error) {
	rows, err := db.Pool.Query(ctx,
		`select id, title, status, created_at from tasks where status='open' order by id limit $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Task, error) {
		var t Task
		return t, r.Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt)
	})
}

// CompleteTask marks an open task done and emits task.done. ErrNotFound if
// the task does not exist or is not open.
func (db *DB) CompleteTask(ctx context.Context, id int64, now time.Time) (Task, error) {
	var t Task
	err := db.withEvents(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`update tasks set status='done', completed_at=$2 where id=$1 and status='open'
			 returning id, title, status, created_at, completed_at`, id, now,
		).Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt, &t.CompletedAt)
		if err != nil {
			return notFound(err)
		}
		return insertEvent(ctx, tx, "task.done", map[string]any{"task_id": t.ID, "title": t.Title})
	})
	return t, err
}
