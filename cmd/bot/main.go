// agentd — Telegram-бот, очередь задач и цикл агента в одном процессе.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	_ "time/tzdata" // зоны типа Asia/Almaty внутри alpine-образа

	tele "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"

	"agentd/internal/agent"
	"agentd/internal/store"
	"agentd/internal/stt"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	fatal := func(msg string, err error) {
		log.Error(msg, "err", err)
		os.Exit(1)
	}

	owner, err := strconv.ParseInt(os.Getenv("OWNER_ID"), 10, 64)
	if err != nil {
		fatal("OWNER_ID", err)
	}

	st, err := store.Open(env("DATABASE_URL", "postgres://agentd:agentd@localhost:5432/agentd?sslmode=disable"))
	if err != nil {
		fatal("open store", err)
	}
	defer st.Close()
	if err := st.ResetRunning(context.Background()); err != nil {
		fatal("reset running", err)
	}

	tb, err := tele.NewBot(tele.Settings{
		Token:   os.Getenv("TELEGRAM_TOKEN"),
		Poller:  &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, _ tele.Context) { log.Error("telegram", "err", err) },
	})
	if err != nil {
		fatal("telegram", err)
	}
	tb.Use(middleware.Whitelist(owner)) // бот личный: чужие сообщения отбрасываются

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	b := &bot{tb: tb, owner: tele.ChatID(owner), store: st, log: log}
	if url := os.Getenv("STT_URL"); url != "" {
		b.stt = &stt.Client{URL: url, Model: env("STT_MODEL", "Systran/faster-whisper-small"), Key: os.Getenv("STT_API_KEY")}
	}
	b.routes(ctx)

	w := &worker{
		store:  st,
		agent:  &agent.Runner{URL: env("WORKER_URL", "http://localhost:8090"), Tools: commaList(os.Getenv("ALLOWED_TOOLS"))},
		notify: b.notify,
		log:    log,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		context.AfterFunc(ctx, tb.Stop) // при Ctrl+C остановить polling
		tb.Start()                      // блокируется, пока не остановят
	}()
	go func() {
		defer wg.Done()
		w.run(ctx)
	}()

	log.Info("agentd started")
	wg.Wait()
	log.Info("agentd stopped")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// commaList разбирает "a, b, c" из env в ["a", "b", "c"].
func commaList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
