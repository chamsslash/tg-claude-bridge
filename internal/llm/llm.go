// Package llm — клиент сервиса к обёртке claude-runner (контейнер claude).
//
// Claude здесь — функция «текст на входе, текст или JSON на выходе»: данные
// собирает сервис, действия выполняет сервис. Что Claude может сделать сам,
// решают флаги обёртки; отсюда — только переключатель Web.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrDisabled — Claude не настроен (CLAUDE_URL пуст).
	ErrDisabled = errors.New("Claude не настроен")
	// ErrBudget — запусков за час уже слишком много.
	ErrBudget = errors.New("лимит запусков Claude за час исчерпан")
)

type Request struct {
	System string          `json:"system"`
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Images []Image         `json:"images,omitempty"`
	// Web — поиск и чтение страниц в этом запуске. Только еда: в запросах с
	// письмами и приглашениями его нет никогда.
	Web bool `json:"web,omitempty"`
}

// Image — картинка для Claude: media_type («image/jpeg») и данные в base64.
type Image struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type Response struct {
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured,omitempty"`
}

type Client struct {
	url    string
	http   *http.Client
	budget *Budget
	report func(error)
}

// New: url — адрес обёртки (http://claude:8090); пустой — Claude выключен.
// timeout — сколько ждать ответа: обёртка ставит запуски в очередь.
func New(url string, budget *Budget, timeout time.Duration) *Client {
	return &Client{url: strings.TrimRight(url, "/"), http: &http.Client{Timeout: timeout}, budget: budget, report: func(error) {}}
}

// Enabled — Claude настроен.
func (c *Client) Enabled() bool { return c != nil && c.url != "" }

// OnResult подписывает на исход каждого запуска — им питается здоровье
// «Claude»: иначе протухший токен выглядел бы просто сводками без пересказа.
func (c *Client) OnResult(f func(error)) { c.report = f }

func (c *Client) Run(ctx context.Context, req Request) (*Response, error) {
	if !c.Enabled() {
		return nil, ErrDisabled
	}
	if !c.budget.Allow() {
		return nil, ErrBudget
	}
	resp, err := c.run(ctx, req)
	if ctx.Err() == nil {
		c.report(err)
	}
	return resp, err
}

func (c *Client) run(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("обёртка Claude: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return nil, fmt.Errorf("обёртка Claude: %d %s", resp.StatusCode, e.Error)
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("обёртка Claude: ответ не JSON: %w", err)
	}
	return &out, nil
}

// JSON запускает Claude со схемой и раскладывает ответ в v. Структурированный
// ответ CLI — в приоритете; если его нет, берётся JSON из текста ответа.
func (c *Client) JSON(ctx context.Context, system, prompt string, schema json.RawMessage, v any) error {
	return c.Decode(ctx, Request{System: system, Prompt: prompt, Schema: schema}, v)
}

// Decode — JSON для произвольного запроса, например с картинками.
func (c *Client) Decode(ctx context.Context, req Request, v any) error {
	resp, err := c.Run(ctx, req)
	if err != nil {
		return err
	}
	raw := []byte(resp.Structured)
	if len(raw) == 0 || string(raw) == "null" {
		raw = extractJSON(resp.Result)
	}
	if raw == nil {
		return errors.New("Claude не вернул JSON")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("ответ Claude не по схеме: %w", err)
	}
	return nil
}

// extractJSON — объект из текста: от первой «{» до последней «}», чтобы
// пережить обёртку ```json и пояснения вокруг.
func extractJSON(s string) []byte {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return nil
	}
	return []byte(s[i : j+1])
}
