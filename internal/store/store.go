// Package store — очередь задач в SQLite. Пишет в неё только agentd.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

const (
	New       = "new" // в очереди: новая задача или ретрай после ошибки
	Running   = "running"
	Waiting   = "waiting" // ждёт deadline
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Время в базе — текст в формате SQLite datetime(), всегда UTC: '2026-09-27 11:06:00'.
// Читается глазами, а сравнение строк совпадает с хронологией (формат фиксированной
// ширины, одна зона). Поэтому в SQL можно писать deadline <= datetime('now').
const timeLayout = "2006-01-02 15:04:05"

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id       INTEGER PRIMARY KEY,
	goal     TEXT    NOT NULL,
	status   TEXT    NOT NULL,
	deadline TEXT, -- UTC, NULL — не задан
	attempts INTEGER NOT NULL DEFAULT 0,
	result   TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS events (
	id      INTEGER PRIMARY KEY,
	task_id INTEGER NOT NULL,
	ts      TEXT    NOT NULL DEFAULT (datetime('now')), -- UTC
	kind    TEXT    NOT NULL, -- goal | note | error
	body    TEXT    NOT NULL
);`

type Task struct {
	ID       int64
	Goal     string
	Status   string
	Deadline time.Time
	Attempts int
	Result   string
	Events   []Event // журнал задачи; заполняет только Claim
}

type Event struct {
	TS   time.Time
	Kind string
	Body string
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // один процесс, одно соединение — никаких блокировок
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateTask(ctx context.Context, goal string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO tasks (goal, status) VALUES (?, ?)`, goal, New)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, s.AddEvent(ctx, id, "goal", goal)
}

func (s *Store) AddEvent(ctx context.Context, taskID int64, kind, body string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (task_id, kind, body) VALUES (?, ?, ?)`,
		taskID, kind, body)
	return err
}

// Claim занимает одну задачу, которую пора запускать (new или waiting
// с наступившим deadline), переводит её в running и отдаёт вместе с журналом.
// Всё в одной транзакции: не удалось прочитать журнал — задача остаётся как была.
// ok = false — брать нечего.
func (s *Store) Claim(ctx context.Context) (task Task, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback() // после Commit ничего не делает

	var deadline sql.NullString // NULL, если deadline не задан
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = ?
		WHERE id = (
			SELECT id FROM tasks
			WHERE status = ? OR (status = ? AND deadline <= datetime('now'))
			ORDER BY id LIMIT 1)
		RETURNING id, goal, status, deadline, attempts`,
		Running, New, Waiting,
	).Scan(&task.ID, &task.Goal, &task.Status, &deadline, &task.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	if deadline.Valid {
		if task.Deadline, err = parseTime(deadline.String); err != nil {
			return Task{}, false, err
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT ts, kind, body FROM events WHERE task_id = ? ORDER BY id`, task.ID)
	if err != nil {
		return Task{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&ts, &e.Kind, &e.Body); err != nil {
			return Task{}, false, err
		}
		if e.TS, err = parseTime(ts); err != nil {
			return Task{}, false, err
		}
		task.Events = append(task.Events, e)
	}
	if err := rows.Err(); err != nil {
		return Task{}, false, err
	}
	return task, true, tx.Commit()
}

// Finish сохраняет итог запуска. Только если задача всё ещё running:
// если её отменили, пока работал агент, ok = false и ничего не пишется.
func (s *Store) Finish(ctx context.Context, task Task) (ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = ?, deadline = ?, attempts = ?, result = ?
		WHERE id = ? AND status = ?`,
		task.Status, formatTime(task.Deadline), task.Attempts, task.Result, task.ID, Running)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ResetRunning — на старте: всё, что осталось в running, брошено прошлым запуском.
func (s *Store) ResetRunning(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE status = ?`, New, Running)
	return err
}

func (s *Store) Cancel(ctx context.Context, id int64) (ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ? AND status NOT IN (?, ?, ?)`,
		Cancelled, id, Done, Failed, Cancelled)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Active — незавершённые задачи для /list.
func (s *Store) Active(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, goal, status FROM tasks WHERE status NOT IN (?, ?, ?) ORDER BY id`,
		Done, Failed, Cancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.Goal, &task.Status); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// formatTime — time.Time → текст для базы. Нулевое время — NULL.
func formatTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func parseTime(s string) (time.Time, error) {
	return time.ParseInLocation(timeLayout, s, time.UTC)
}
