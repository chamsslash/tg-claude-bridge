// Package google — Gmail и Google Calendar для сервиса через REST API.
//
// Доступ к Google есть только у сервиса: Claude не получает ни токенов, ни
// инструментов. Gmail — только чтение (gmail.readonly), календарь — события
// основного календаря (calendar.events), без рассылки приглашений.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Scopes — что сервис просит у Google.
var Scopes = []string{
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/calendar.events",
}

var (
	// ErrAuth — refresh-токен отозван или истёк: нужен новый.
	ErrAuth = errors.New("Google не принял refresh-токен — получи новый: tg-bridge google-auth")
	// ErrNotFound — события или письма уже нет.
	ErrNotFound = errors.New("в Google такого нет")
)

const (
	tokenURL     = "https://oauth2.googleapis.com/token"
	gmailBase    = "https://gmail.googleapis.com/gmail/v1/users/me"
	calendarBase = "https://www.googleapis.com/calendar/v3/calendars/primary"
)

// Client — авторизованный клиент: access-токен по refresh-токену, с кешем до
// истечения.
type Client struct {
	id, secret, refresh string
	http                *http.Client

	tokenURL, gmailBase, calendarBase string

	mu     sync.Mutex
	access string
	exp    time.Time
	now    func() time.Time
}

func New(clientID, secret, refresh string, timeout time.Duration) *Client {
	return &Client{id: clientID, secret: secret, refresh: refresh, http: &http.Client{Timeout: timeout},
		tokenURL: tokenURL, gmailBase: gmailBase, calendarBase: calendarBase, now: time.Now}
}

// Enabled — Google настроен.
func (c *Client) Enabled() bool { return c != nil && c.refresh != "" }

func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.access != "" && c.now().Before(c.exp) {
		return c.access, nil
	}
	form := url.Values{
		"client_id":     {c.id},
		"client_secret": {c.secret},
		"refresh_token": {c.refresh},
		"grant_type":    {"refresh_token"},
	}
	var tok struct {
		Access    string `json:"access_token"`
		ExpiresIn int    `json:"expires_in"`
		Error     string `json:"error"`
	}
	if err := postForm(ctx, c.http, c.tokenURL, form, &tok); err != nil {
		return "", err
	}
	if tok.Error == "invalid_grant" {
		return "", ErrAuth
	}
	if tok.Access == "" {
		return "", fmt.Errorf("токен Google: %s", tok.Error)
	}
	c.access = tok.Access
	// Минута запаса: токен не должен истечь посреди запроса.
	c.exp = c.now().Add(time.Duration(tok.ExpiresIn)*time.Second - time.Minute)
	return c.access, nil
}

// postForm шлёт форму и раскладывает JSON ответа — и успешного, и с ошибкой:
// у эндпоинта токенов ошибка тоже JSON.
func postForm(ctx context.Context, hc *http.Client, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("токен Google: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("токен Google: %d, ответ не JSON", resp.StatusCode)
	}
	return nil
}

// do — запрос к API с токеном; out == nil — тело ответа не нужно.
func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Google: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		// Access-токен мог протухнуть раньше срока — следующий запрос возьмёт новый.
		c.mu.Lock()
		c.access = ""
		c.mu.Unlock()
		return fmt.Errorf("Google: 401")
	case resp.StatusCode >= 300:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return fmt.Errorf("Google: %d %s", resp.StatusCode, e.Error.Message)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
