package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentd/internal/store"
)

func TestParse(t *testing.T) {
	ok := map[string]string{
		"done":            "Готово.\n```json\n{\"status\":\"done\",\"result\":\"ok\"}\n```",
		"last block wins": "```json\n{\"status\":\"failed\",\"note\":\"x\"}\n```\n```json\n{\"status\":\"waiting\",\"deadline\":\"2026-09-28T18:00:00+05:00\"}\n```",
	}
	for name, text := range ok {
		if _, err := Parse(text); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	bad := map[string]string{
		"no block":       "Готово!",
		"waiting no dl":  "```json\n{\"status\":\"waiting\"}\n```",
		"unknown status": "```json\n{\"status\":\"maybe\"}\n```",
		"unclosed":       "```json\n{\"status\":\"done\",\"result\":\"ok\"}",
	}
	for name, text := range bad {
		if out, err := Parse(text); err == nil {
			t.Errorf("%s: want error, got %+v", name, out)
		}
	}
}

func TestRun(t *testing.T) {
	var gotTools []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt       string   `json:"prompt"`
			AllowedTools []string `json:"allowed_tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !strings.Contains(req.Prompt, "Задача #7") {
			t.Errorf("prompt = %q, want task goal", req.Prompt)
		}
		gotTools = req.AllowedTools
		json.NewEncoder(w).Encode(struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}{false, "Готово.\n```json\n{\"status\":\"done\",\"result\":\"ок\"}\n```"})
	}))
	defer srv.Close()

	r := &Runner{URL: srv.URL, Tools: []string{"mcp__general-mcp__send_message"}}
	out, err := r.Run(context.Background(), store.Task{ID: 7, Goal: "сделать"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "done" || out.Result != "ок" {
		t.Errorf("outcome = %+v, want done/ок", out)
	}
	if len(gotTools) != 1 || gotTools[0] != "mcp__general-mcp__send_message" {
		t.Errorf("allowed_tools = %v", gotTools)
	}
}

func TestRunWorkerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("claude упал"))
	}))
	defer srv.Close()

	r := &Runner{URL: srv.URL}
	if _, err := r.Run(context.Background(), store.Task{ID: 7, Goal: "g"}); err == nil || !strings.Contains(err.Error(), "claude упал") {
		t.Errorf("err = %v, want worker error", err)
	}
}
