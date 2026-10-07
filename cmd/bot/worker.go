package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentd/internal/agent"
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
			task.Status = store.Failed
			msg = fmt.Sprintf("❌ #%d не выполнена после %d попыток: %v", task.ID, maxAttempts, err)
		}
	} else {
		w.store.AddEvent(ctx, task.ID, "note", outcome.Note)
		task.Status = outcome.Status
		switch outcome.Status {
		case store.Done:
			task.Result = outcome.Result
			msg = fmt.Sprintf("✅ #%d выполнена.\n\n%s", task.ID, outcome.Result)
		case store.Waiting:
			task.Deadline = outcome.Deadline
		case store.Failed:
			msg = fmt.Sprintf("❌ #%d не выполнена: %s", task.ID, outcome.Note)
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
	log.Info("agent finished", "status", task.Status)
	if msg != "" {
		w.notify(msg)
	}
}
