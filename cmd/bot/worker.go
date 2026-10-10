package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentd/internal/agent"
	"agentd/internal/schedule"
	"agentd/internal/store"
)

const (
	tick         = 10 * time.Second
	agentTimeout = 31 * time.Minute // больше DEFAULT_TIMEOUT_SEC claude-worker (1800 с), чтобы успел прийти его 504
	maxAttempts  = 3
)

// worker раз в tick разбирает очередь, задачи прогоняет через агента по одной.
type worker struct {
	store  *store.Store
	agent  *agent.Runner
	notify func(text string)
	log    *slog.Logger
}

// run разбирает готовые задачи одну за другой, пока очередь не опустеет,
// затем ждёт следующего тика. Агент работает дольше тика — очередь подождёт.
func (w *worker) run(ctx context.Context) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for ctx.Err() == nil {
		task, ok, err := w.store.Claim(ctx)
		if err != nil {
			w.log.Error("claim", "err", err)
		} else if ok {
			w.handle(ctx, task)
			continue // очередь может быть непустой — не ждём тика
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *worker) handle(ctx context.Context, task store.Task) {
	log := w.log.With("task", task.ID)

	runCtx, cancel := context.WithTimeout(ctx, agentTimeout)
	outcome, err := w.agent.Run(runCtx, task)
	defer cancel()

	if ctx.Err() != nil {
		return // процесс останавливается; задача осталась running, на старте вернётся в очередь
	}

	var msg string
	if err != nil {
		log.Warn("agent failed", "err", err)
		w.store.AddEvent(ctx, task.ID, "error", err.Error())
		task.Attempts++
		task.Status = store.New // ретрай
		if task.Attempts >= maxAttempts {
			if task.Schedule == "" {
				task.Status = store.Failed
				msg = fmt.Sprintf("❌ #%d не выполнена после %d попыток: %v", task.ID, maxAttempts, err)
			} else { // повторяющаяся не умирает — ждём следующего окна
				reschedule(&task, time.Now())
				msg = fmt.Sprintf("⚠️ #%d не удалась (%v), следующий запуск %s.", task.ID, err, stamp(task.Deadline))
			}
		}
	} else {
		w.store.AddEvent(ctx, task.ID, "note", outcome.Note)
		task.Status = outcome.Status
		switch outcome.Status {
		case store.Done:
			task.Result = outcome.Result
			if task.Schedule == "" {
				msg = fmt.Sprintf("✅ #%d выполнена.\n\n%s", task.ID, outcome.Result)
			} else {
				reschedule(&task, time.Now())
				msg = fmt.Sprintf("🔁 #%d готово, следующий запуск %s.\n\n%s", task.ID, stamp(task.Deadline), outcome.Result)
			}
		case store.Waiting:
			task.Deadline = outcome.Deadline
		case store.Failed:
			if task.Schedule == "" {
				msg = fmt.Sprintf("❌ #%d не выполнена: %s", task.ID, outcome.Note)
			} else {
				reschedule(&task, time.Now())
				msg = fmt.Sprintf("⚠️ #%d не выполнена: %s\nСледующий запуск %s.", task.ID, outcome.Note, stamp(task.Deadline))
			}
		}
	}

	ok, err := w.store.Finish(ctx, task)
	if err != nil {
		log.Error("finish", "err", err)
		return
	}
	if !ok {
		return // задачу отменили, пока работал агент
	}
	log.Info("agent finished", "status", task.Status, "next", task.Deadline)
	if msg != "" {
		w.notify(msg)
	}
}

// reschedule назначает повторяющейся задаче следующий запуск и сбрасывает попытки.
// Расписание проверяется при создании, так что ошибка тут возможна только если
// spec в базе повреждён — тогда задачу приходится добить.
func reschedule(task *store.Task, now time.Time) {
	next, err := schedule.Next(task.Schedule, now)
	if err != nil {
		task.Status = store.Failed
		return
	}
	task.Status = store.Waiting
	task.Deadline = next
	task.Attempts = 0
}

func stamp(t time.Time) string { return t.Local().Format("02.01 15:04") }
