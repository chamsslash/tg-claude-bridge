// Package telegram — минимальный клиент Bot API: длинный опрос и отправка.
// Реализованы только два метода, которые нужны мосту.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// MessageLimit — потолок Telegram на длину одного сообщения.
const MessageLimit = 4096

type Client struct {
	token       string
	pollTimeout time.Duration
	http        *http.Client
}

// New собирает клиент. Таймаут HTTP заведомо больше таймаута опроса: длинный
// опрос по определению висит без ответа до самого конца, и таймаут клиента
// обязан пережить это ожидание, иначе каждый пустой цикл выглядел бы обрывом.
func New(token string, pollTimeout time.Duration) *Client {
	return &Client{
		token:       token,
		pollTimeout: pollTimeout,
		http:        &http.Client{Timeout: pollTimeout + 30*time.Second},
	}
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	Text string `json:"text"`
	Chat Chat   `json:"chat"`
}

type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
	Edited   *Message `json:"edited_message"`
}

// Body возвращает содержательную часть апдейта: правку сообщения обрабатываем
// так же, как новое, — человек поправил опечатку в команде, а не отменил её.
func (u Update) Body() *Message {
	if u.Message != nil {
		return u.Message
	}
	return u.Edited
}

type apiError struct {
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %d: %s", e.Code, e.Description)
}

func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := "https://api.telegram.org/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("%s: неразборчивый ответ: %w", method, err)
	}
	if !envelope.OK {
		return &apiError{Code: resp.StatusCode, Description: envelope.Description}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

// GetUpdates ждёт новые сообщения. Возвращает пустой срез, если за время
// ожидания ничего не пришло, — это штатный исход, а не ошибка.
func (c *Client) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(c.pollTimeout.Seconds()),
		// Просим только сообщения: остальные типы апдейтов мост не обрабатывает,
		// и тянуть их значило бы двигать offset по событиям, которые мы молча
		// выбрасываем.
		"allowed_updates": []string{"message", "edited_message"},
	}, &out)
	return out, err
}

// Send отправляет текст, при необходимости разбивая его на несколько
// сообщений: Telegram режет всё длиннее MessageLimit.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	for i, part := range Split(text, MessageLimit) {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		err := c.call(ctx, "sendMessage", map[string]any{
			"chat_id":                  chatID,
			"text":                     part,
			"disable_web_page_preview": true,
		}, nil)
		if err != nil {
			return err
		}
	}
	return nil
}
