// Package yazio — дневник питания Yazio через API его мобильного приложения.
//
// Публичного API у Yazio нет: это тот же закрытый API v22, в который ходит
// приложение (справочник — github.com/yazio-community/yazio-api-specification).
// Мост пишет в дневник только «быстрый ввод» (simple_product): название и
// КБЖУ порции, без поиска продуктов в базе.
package yazio

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/msk"
)

const (
	baseURL = "https://yzapi.yazio.com/v22"

	// DefaultUserAgent — версия приложения, которую Yazio пускает. Без неё
	// всё, кроме входа, отвечает 403 version_blocked; когда эту версию
	// заблокируют, строку меняют в YAZIO_USER_AGENT.
	DefaultUserAgent = "YAZIO/26.30.1 (com.yazio.ios.YAZIO; build:2607271240; iOS 27.0.0) Ktor"

	// Пара OAuth-клиента мобильного приложения: одна на все установки,
	// опубликована в спецификации, секретом не является.
	clientID     = "3_5rbw4kehpugw8ogsc8ck8oo4ogswgckcskc04gcg8kk8k48ssw"
	clientSecret = "25gdtt1hvdi8gwowoww4oo88sgsw0oo04o0og0kkgwwks8k0k"
)

var (
	// ErrAuth — Yazio не принял логин или пароль.
	ErrAuth = errors.New("Yazio не принял логин или пароль — проверь YAZIO_USERNAME и YAZIO_PASSWORD")
	// ErrVersionBlocked — Yazio перестал пускать версию из User-Agent.
	ErrVersionBlocked = errors.New("Yazio не пускает эту версию приложения — обнови YAZIO_USER_AGENT")
)

// Daytime — приём пищи в дневнике.
const (
	Breakfast = "breakfast"
	Lunch     = "lunch"
	Dinner    = "dinner"
	Snack     = "snack"
)

// Entry — запись «быстрого ввода»: КБЖУ — на всю порцию.
type Entry struct {
	ID      string
	Time    time.Time
	Daytime string
	Name    string
	Kcal    float64
	Protein float64
	Fat     float64
	Carb    float64
}

type Client struct {
	user, pass, ua string
	http           *http.Client
	base           string

	mu    sync.Mutex
	token string
}

// New: пустые user и pass — Yazio не настроен; пустой ua — DefaultUserAgent.
func New(user, pass, ua string, timeout time.Duration) *Client {
	if ua == "" {
		ua = DefaultUserAgent
	}
	return &Client{user: user, pass: pass, ua: ua, http: &http.Client{Timeout: timeout}, base: baseURL}
}

// Enabled — Yazio настроен.
func (c *Client) Enabled() bool { return c != nil && c.user != "" && c.pass != "" }

// Add пишет запись в дневник.
func (c *Client) Add(ctx context.Context, e Entry) error {
	item := map[string]any{
		"id":      e.ID,
		"date":    e.Time.In(msk.Location).Format("2006-01-02 15:04:05"),
		"daytime": e.Daytime,
		"name":    e.Name,
		"nutrients": map[string]float64{
			"energy.energy":    e.Kcal,
			"nutrient.protein": e.Protein,
			"nutrient.fat":     e.Fat,
			"nutrient.carb":    e.Carb,
		},
		// Так отправляет и приложение; как Yazio покажет true, не проверено.
		"is_ai_generated": false,
	}
	body := map[string]any{"products": []any{}, "recipe_portions": []any{}, "simple_products": []any{item}}
	return c.do(ctx, http.MethodPost, "/user/consumed-items", body, nil)
}

// Delete удаляет запись. Yazio отвечает успехом и тогда, когда удалять было
// нечего, — исход проверяют через Day.
func (c *Client) Delete(ctx context.Context, id string) error {
	// Строка, а не массив: так принимает API.
	return c.do(ctx, http.MethodDelete, "/user/consumed-items", map[string]string{"simple_products": id}, nil)
}

// Day — id записей «быстрого ввода» за день (ГГГГ-ММ-ДД).
func (c *Client) Day(ctx context.Context, day string) ([]string, error) {
	var out struct {
		Simple []struct {
			ID string `json:"id"`
		} `json:"simple_products"`
	}
	if err := c.do(ctx, http.MethodGet, "/user/consumed-items?date="+url.QueryEscape(day), nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Simple))
	for _, s := range out.Simple {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// NewID — UUID v4 для новой записи: id генерирует клиент.
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// do — запрос с токеном. Токен живёт двое суток и только в памяти; на 401
// сервис входит заново один раз.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := c.login(ctx)
		if err != nil {
			return err
		}
		code, raw, err := c.send(ctx, method, path, body, tok)
		if err != nil {
			return err
		}
		switch {
		case code == http.StatusUnauthorized && attempt == 0:
			c.mu.Lock()
			if c.token == tok {
				c.token = ""
			}
			c.mu.Unlock()
			continue
		case code == http.StatusForbidden && strings.Contains(string(raw), "version_blocked"):
			return ErrVersionBlocked
		case code >= 300:
			return fmt.Errorf("Yazio: %s %s: %d", method, strings.SplitN(path, "?", 2)[0], code)
		}
		if out == nil || len(raw) == 0 {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
}

func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	body := map[string]string{
		"grant_type":    "password",
		"username":      c.user,
		"password":      c.pass,
		"client_id":     clientID,
		"client_secret": clientSecret,
	}
	code, raw, err := c.send(ctx, http.MethodPost, "/oauth/token", body, "")
	if err != nil {
		return "", err
	}
	if code == http.StatusBadRequest || code == http.StatusUnauthorized {
		return "", ErrAuth
	}
	var tok struct {
		Access string `json:"access_token"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &tok) != nil || tok.Access == "" {
		return "", fmt.Errorf("Yazio: вход: %d", code)
	}
	c.token = tok.Access
	return c.token, nil
}

func (c *Client) send(ctx context.Context, method, path string, body any, tok string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "ru-RU")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("Yazio: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, raw, err
}
