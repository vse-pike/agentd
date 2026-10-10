// Package store — очередь задач в Postgres. Пишет в неё только agentd.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер "pgx" для database/sql
)

const (
	New       = "new" // в очереди: новая задача или ретрай после ошибки
	Running   = "running"
	Waiting   = "waiting" // ждёт deadline
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	goal     TEXT    NOT NULL,
	status   TEXT    NOT NULL,
	deadline TIMESTAMPTZ, -- NULL — не задан
	attempts INTEGER NOT NULL DEFAULT 0,
	result   TEXT    NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS events (
	id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	task_id BIGINT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	ts      TIMESTAMPTZ NOT NULL DEFAULT now(),
	kind    TEXT NOT NULL, -- goal | note | error
	body    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_task ON events(task_id);`

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

// Open подключается по URL вида postgres://user:pass@host:5432/db?sslmode=disable.
func Open(url string) (*Store, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4) // воркер один, но коннерты нужны и боту
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateTask(ctx context.Context, goal string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tasks (goal, status) VALUES ($1, $2) RETURNING id`, goal, New).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, s.AddEvent(ctx, id, "goal", goal)
}

func (s *Store) AddEvent(ctx context.Context, taskID int64, kind, body string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (task_id, kind, body) VALUES ($1, $2, $3)`,
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

	var deadline sql.NullTime // NULL, если deadline не задан
	err = tx.QueryRowContext(ctx, `
		UPDATE tasks SET status = $1
		WHERE id = (
			SELECT id FROM tasks
			WHERE status = $2 OR (status = $3 AND deadline <= now())
			ORDER BY id LIMIT 1
			FOR UPDATE SKIP LOCKED)
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
		task.Deadline = deadline.Time
	}

	rows, err := tx.QueryContext(ctx, `SELECT ts, kind, body FROM events WHERE task_id = $1 ORDER BY id`, task.ID)
	if err != nil {
		return Task{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.TS, &e.Kind, &e.Body); err != nil {
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
		UPDATE tasks SET status = $1, deadline = $2, attempts = $3, result = $4
		WHERE id = $5 AND status = $6`,
		task.Status, nullTime(task.Deadline), task.Attempts, task.Result, task.ID, Running)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ResetRunning — на старте: всё, что осталось в running, брошено прошлым запуском.
func (s *Store) ResetRunning(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE status = $2`, New, Running)
	return err
}

func (s *Store) Cancel(ctx context.Context, id int64) (ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE tasks SET status = $1 WHERE id = $2 AND status NOT IN ($3, $4, $5)`,
		Cancelled, id, Done, Failed, Cancelled)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Active — незавершённые задачи для /list.
func (s *Store) Active(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, goal, status FROM tasks WHERE status NOT IN ($1, $2, $3) ORDER BY id`,
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

// nullTime — time.Time → NULL, если нулевое.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
