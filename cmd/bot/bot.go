package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"agentd/internal/store"
	"agentd/internal/stt"
)

type bot struct {
	tb    *tele.Bot
	owner tele.ChatID
	store *store.Store
	stt   *stt.Client // nil — голосовые выключены
	log   *slog.Logger
}

func (b *bot) routes(ctx context.Context) {
	b.tb.Handle("/start", func(c tele.Context) error {
		return c.Send("Пиши задачу текстом или голосовым — поставлю в очередь.\n" +
			"Не выполнена — поставь новую с уточнением.\n\n/list — активные задачи\n/cancel <id> — отменить")
	})
	b.tb.Handle("/list", func(c tele.Context) error { return b.list(ctx, c) })
	b.tb.Handle("/cancel", func(c tele.Context) error { return b.cancel(ctx, c) })
	b.tb.Handle(tele.OnText, func(c tele.Context) error { return b.text(ctx, c) })
	b.tb.Handle(tele.OnVoice, func(c tele.Context) error { return b.voice(ctx, c) })
}

// notify — отправить владельцу. Им пользуется worker.
func (b *bot) notify(text string) {
	if r := []rune(text); len(r) > 4000 { // лимит Telegram — 4096 символов
		text = string(r[:4000]) + "…"
	}
	if _, err := b.tb.Send(b.owner, text); err != nil {
		b.log.Error("notify", "err", err)
	}
}

func (b *bot) text(ctx context.Context, c tele.Context) error {
	id, err := b.store.CreateTask(ctx, c.Text())
	if err != nil {
		return err
	}
	return c.Reply(fmt.Sprintf("Задача #%d в очереди.", id))
}

func (b *bot) voice(ctx context.Context, c tele.Context) error {
	if b.stt == nil {
		return c.Send("Голосовые не поддерживаются: STT_URL не задан.")
	}
	rc, err := b.tb.File(&c.Message().Voice.File)
	if err != nil {
		return err
	}
	defer rc.Close()

	text, err := b.stt.Transcribe(ctx, rc, "voice.ogg")
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return c.Send("Не разобрал голосовое.")
	}
	id, err := b.store.CreateTask(ctx, text)
	if err != nil {
		return err
	}
	return c.Reply(fmt.Sprintf("Задача #%d в очереди:\n\n%s", id, text))
}

func (b *bot) list(ctx context.Context, c tele.Context) error {
	tasks, err := b.store.Active(ctx)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return c.Send("Активных задач нет.")
	}
	var sb strings.Builder
	for _, task := range tasks {
		fmt.Fprintf(&sb, "#%d [%s] %s\n", task.ID, task.Status, task.Goal)
	}
	return c.Send(sb.String())
}

func (b *bot) cancel(ctx context.Context, c tele.Context) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(c.Message().Payload, "#"), 10, 64)
	if err != nil {
		return c.Send("Формат: /cancel 12")
	}
	ok, err := b.store.Cancel(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return c.Send(fmt.Sprintf("Задача #%d не найдена или уже завершена.", id))
	}
	// Если агент сейчас работает над ней — доработает, но результат не запишется.
	return c.Send(fmt.Sprintf("Задача #%d отменена.", id))
}
