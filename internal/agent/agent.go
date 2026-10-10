// Package agent готовит промпт, отдаёт его claude-worker'у и разбирает решение.
// Агент про базу задач ничего не знает: на вход — цель и журнал, на выход — JSON.
package agent

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentd/internal/schedule"
	"agentd/internal/store"
)

// Outcome — решение агента, последний ```json-блок его ответа.
type Outcome struct {
	Status   string    `json:"status"` // done | waiting | failed
	Note     string    `json:"note"`
	Deadline time.Time `json:"deadline"`
	Result   string    `json:"result"`
}

//go:embed prompt.txt
var instructions string

// Runner шлёт промпт в claude-worker (см. ~/IT/Projects/claude-worker) и разбирает ответ.
type Runner struct {
	URL   string   // http://claude-worker:8090
	Tools []string // --allowed-tools
}

// Run блокируется, пока worker не вернёт ответ агента. Таймаут — через ctx.
func (r *Runner) Run(ctx context.Context, t store.Task) (Outcome, error) {
	reqBody, err := json.Marshal(struct {
		Prompt       string   `json:"prompt"`
		AllowedTools []string `json:"allowed_tools,omitempty"`
	}{prompt(t), r.Tools})
	if err != nil {
		return Outcome{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL+"/run", bytes.NewReader(reqBody))
	if err != nil {
		return Outcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("worker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Outcome{}, fmt.Errorf("worker: %s: %s", resp.Status, detail)
	}

	// worker отдаёт stdout claude --output-format json: {"is_error": false, "result": "<текст>", ...}
	var out struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Outcome{}, err
	}
	if out.IsError {
		return Outcome{}, errors.New(out.Result)
	}
	return Parse(out.Result)
}

func prompt(task store.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Задача #%d. Сейчас %s.\n\n", task.ID, time.Now().Format(time.RFC3339))
	if task.Schedule != "" {
		fmt.Fprintf(&b, "Задача повторяется по расписанию (%s): это один очередной запуск, до завершения не обязательно успевать всё.\n\n", schedule.Describe(task.Schedule))
	}
	fmt.Fprintf(&b, "ЦЕЛЬ\n%s\n\nЖУРНАЛ\n", task.Goal)
	for _, e := range task.Events {
		fmt.Fprintf(&b, "- [%s] %s: %s\n", e.TS.Local().Format("2006-01-02 15:04"), e.Kind, e.Body)
	}
	b.WriteString("\n" + instructions)
	return b.String()
}

// Parse достаёт последний ```json-блок из ответа и проверяет обязательные поля.
func Parse(text string) (Outcome, error) {
	start := strings.LastIndex(text, "```json")
	if start < 0 {
		return Outcome{}, errors.New("в ответе нет ```json-блока")
	}
	body, _, found := strings.Cut(text[start+len("```json"):], "```")
	if !found {
		return Outcome{}, errors.New("```json-блок не закрыт")
	}

	var out Outcome
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return Outcome{}, err
	}
	switch {
	case out.Status == "done" && out.Result == "",
		out.Status == "waiting" && out.Deadline.IsZero(),
		out.Status == "failed" && out.Note == "":
		return Outcome{}, fmt.Errorf("status %q без обязательного поля", out.Status)
	case out.Status != "done" && out.Status != "waiting" && out.Status != "failed":
		return Outcome{}, fmt.Errorf("неизвестный status %q", out.Status)
	}
	return out, nil
}
