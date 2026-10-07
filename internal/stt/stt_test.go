package stt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTranscribe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		if got := r.FormValue("model"); got != "Systran/faster-whisper-small" {
			t.Errorf("model = %q", got)
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("file: %v", err)
			return
		}
		defer file.Close()
		if hdr.Filename != "voice.ogg" {
			t.Errorf("filename = %q", hdr.Filename)
		}
		json.NewEncoder(w).Encode(struct {
			Text string `json:"text"`
		}{"купить хлеб"})
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, Model: "Systran/faster-whisper-small"}
	text, err := c.Transcribe(context.Background(), strings.NewReader("audio"), "voice.ogg")
	if err != nil {
		t.Fatal(err)
	}
	if text != "купить хлеб" {
		t.Errorf("text = %q", text)
	}
}

func TestTranscribeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail": "model not found"}`))
	}))
	defer srv.Close()

	c := &Client{URL: srv.URL, Model: "m"}
	if _, err := c.Transcribe(context.Background(), strings.NewReader("a"), "v.ogg"); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Errorf("err = %v, want model not found", err)
	}
}
