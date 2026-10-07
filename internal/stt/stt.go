// Package stt — клиент OpenAI-совместимой расшифровки речи (/audio/transcriptions):
// локальный Speaches, Groq, OpenAI — всё одно и то же API.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

type Client struct {
	URL   string // база до /v1, например http://whisper:8001/v1
	Model string // например Systran/faster-whisper-small
	Key   string // пуст — без Authorization (локальный сервер)
}

// Transcribe отправляет аудиофайл и возвращает расшифрованный текст.
func (c *Client) Transcribe(ctx context.Context, audio io.Reader, filename string) (string, error) {
	// тело запроса — multipart-форма: файл + название модели
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, audio); err != nil {
		return "", err
	}
	if err := mw.WriteField("model", c.Model); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("stt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("stt: %s: %s", resp.Status, detail)
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Text, nil
}
