package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tele "gopkg.in/telebot.v3"

	"agentd/internal/schedule"
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
			"Не выполнена — поставь новую с уточнением.\n" +
			"Повторяющаяся: первая строка «ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДЫЙ ДЕНЬ - 09:00».\n\n" +
			"/list — активные задачи\n/cancel <id> — отменить")
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
	_, reply, err := b.create(ctx, c.Text())
	if err != nil {
		return c.Send(err.Error())
	}
	return c.Reply(reply)
}

// create разбирает текст задачи (включая заголовок повторения) и ставит её в очередь.
func (b *bot) create(ctx context.Context, text string) (id int64, reply string, err error) {
	spec, body, err := schedule.Parse(text)
	if err != nil {
		return 0, "", err
	}
	id, err = b.store.CreateTask(ctx, body, spec)
	if err != nil {
		return 0, "", err
	}
	if spec != "" {
		return id, fmt.Sprintf("Задача #%d в очереди (повторяется: %s).", id, schedule.Describe(spec)), nil
	}
	return id, fmt.Sprintf("Задача #%d в очереди.", id), nil
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
	_, reply, err := b.create(ctx, text)
	if err != nil {
		return c.Send(err.Error())
	}
	return c.Reply(reply + "\n\n" + text)
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
		title := cutLine(task.Goal, 60)
		if task.Schedule != "" {
			next := ""
			if !task.Deadline.IsZero() {
				next = ", след. " + stamp(task.Deadline)
			}
			fmt.Fprintf(&sb, "#%d [🔁 %s%s] %s\n", task.ID, schedule.Describe(task.Schedule), next, title)
		} else {
			fmt.Fprintf(&sb, "#%d [%s] %s\n", task.ID, task.Status, title)
		}
	}
	return c.Send(sb.String())
}

// cutLine — первая строка текста, обрезанная до n символов: цели бывают длинные.
func cutLine(s string, n int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
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
