// Package yazio — дневник питания Yazio через API его мобильного приложения.
//
// Публичного API у Yazio нет: это тот же закрытый API v22, в который ходит
// приложение (справочник — github.com/yazio-community/yazio-api-specification).
// Мост пишет в дневник «быстрый ввод» (simple_product: название и КБЖУ
// порции) или продукт из базы Yazio (product: id продукта и вес), ищет
// продукты и читает итог дня с целями.
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

// Entry — запись в дневник. Без ProductID — «быстрый ввод»: название и КБЖУ
// на всю порцию. С ProductID — продукт из базы Yazio весом Amount (г или мл),
// КБЖУ Yazio считает сам, а Name и КБЖУ здесь — для ответа владельцу.
type Entry struct {
	ID        string
	Time      time.Time
	Daytime   string
	Name      string
	Kcal      float64
	Protein   float64
	Fat       float64
	Carb      float64
	ProductID string
	Amount    float64
}

// Product — продукт из базы Yazio: КБЖУ на 1 г или 1 мл (Unit — g или ml).
type Product struct {
	ID, Name, Producer, Unit string
	Kcal, Protein, Fat, Carb float64
}

type Client struct {
	user, pass, ua string
	http           *http.Client
	base           string

	mu      sync.Mutex
	token   string
	profile *profile
}

// profile — что из профиля нужно поиску: выдача зависит от пола и страны
// базы продуктов.
type profile struct {
	Sex     string `json:"sex"`
	Country string `json:"food_database_country"`
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

// Add пишет записи в дневник одним запросом: блюдо и напиток с одного фото
// появляются вместе или не появляются вовсе.
func (c *Client) Add(ctx context.Context, es []Entry) error {
	products, simple := []any{}, []any{}
	for _, e := range es {
		if e.ProductID != "" {
			products = append(products, product(e))
		} else {
			simple = append(simple, simpleProduct(e))
		}
	}
	body := map[string]any{"products": products, "recipe_portions": []any{}, "simple_products": simple}
	return c.do(ctx, http.MethodPost, "/user/consumed-items", body, nil)
}

// product — продукт из базы весом в его единицах; без порции («бутылка»),
// так Yazio не пересчитывает вес сам. Проверено 29.10.2026.
func product(e Entry) map[string]any {
	return map[string]any{
		"id":               e.ID,
		"product_id":       e.ProductID,
		"amount":           e.Amount,
		"serving":          nil,
		"serving_quantity": nil,
		"daytime":          e.Daytime,
		"date":             e.Time.In(msk.Location).Format("2006-01-02 15:04:05"),
	}
}

func simpleProduct(e Entry) map[string]any {
	return map[string]any{
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
}

// Delete удаляет запись: product — продукт из базы, иначе «быстрый ввод».
// Yazio отвечает успехом и тогда, когда удалять было нечего, — исход
// проверяют через Day.
func (c *Client) Delete(ctx context.Context, id string, product bool) error {
	kind := "simple_products"
	if product {
		kind = "products"
	}
	// Строка, а не массив: так принимает API.
	return c.do(ctx, http.MethodDelete, "/user/consumed-items", map[string]string{kind: id}, nil)
}

// Day — id записей за день (ГГГГ-ММ-ДД): «быстрого ввода» и продуктов.
func (c *Client) Day(ctx context.Context, day string) ([]string, error) {
	type ref struct {
		ID string `json:"id"`
	}
	var out struct {
		Products []ref `json:"products"`
		Simple   []ref `json:"simple_products"`
	}
	if err := c.do(ctx, http.MethodGet, "/user/consumed-items?date="+url.QueryEscape(day), nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Products)+len(out.Simple))
	for _, r := range append(out.Products, out.Simple...) {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// Search ищет продукты в базе Yazio, лучшие — первыми. Поиск ищет все слова
// запроса в названии: «шорли клубника мята» найдёт, а с брендом, которого
// нет в названии, — уже ничего.
func (c *Client) Search(ctx context.Context, query string) ([]Product, error) {
	p, err := c.me(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"query": {query}, "sex": {p.Sex}, "countries": {p.Country}}
	var out []struct {
		ID        string             `json:"product_id"`
		Name      string             `json:"name"`
		Producer  string             `json:"producer"`
		Unit      string             `json:"base_unit"`
		Nutrients map[string]float64 `json:"nutrients"`
	}
	if err := c.do(ctx, http.MethodGet, "/products/search?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	ps := make([]Product, 0, len(out))
	for _, o := range out {
		ps = append(ps, Product{ID: o.ID, Name: o.Name, Producer: o.Producer, Unit: o.Unit,
			Kcal: o.Nutrients["energy.energy"], Protein: o.Nutrients["nutrient.protein"],
			Fat: o.Nutrients["nutrient.fat"], Carb: o.Nutrients["nutrient.carb"]})
	}
	return ps, nil
}

// me — профиль владельца; читается один раз.
func (c *Client) me(ctx context.Context) (profile, error) {
	c.mu.Lock()
	p := c.profile
	c.mu.Unlock()
	if p != nil {
		return *p, nil
	}
	var out profile
	if err := c.do(ctx, http.MethodGet, "/user", nil, &out); err != nil {
		return profile{}, err
	}
	if out.Sex == "" || out.Country == "" {
		return profile{}, errors.New("Yazio: в профиле нет пола или страны базы продуктов")
	}
	c.mu.Lock()
	c.profile = &out
	c.mu.Unlock()
	return out, nil
}

// Summary — итог дня, как на главном экране приложения: съедено и цели.
type Summary struct {
	Kcal, Protein, Fat, Carb                 float64
	KcalGoal, ProteinGoal, FatGoal, CarbGoal float64
}

// Summary — итог дня (ГГГГ-ММ-ДД). Сожжённое на тренировках прибавляется к
// цели по калориям, если так настроено в приложении: остаток тогда тот же,
// что видит владелец.
func (c *Client) Summary(ctx context.Context, day string) (Summary, error) {
	var out struct {
		ActivityEnergy        float64            `json:"activity_energy"`
		ConsumeActivityEnergy bool               `json:"consume_activity_energy"`
		Goals                 map[string]float64 `json:"goals"`
		Meals                 map[string]struct {
			Nutrients map[string]float64 `json:"nutrients"`
		} `json:"meals"`
	}
	if err := c.do(ctx, http.MethodGet, "/user/widgets/daily-summary?date="+url.QueryEscape(day), nil, &out); err != nil {
		return Summary{}, err
	}
	s := Summary{
		KcalGoal:    out.Goals["energy.energy"],
		ProteinGoal: out.Goals["nutrient.protein"],
		FatGoal:     out.Goals["nutrient.fat"],
		CarbGoal:    out.Goals["nutrient.carb"],
	}
	if out.ConsumeActivityEnergy {
		s.KcalGoal += out.ActivityEnergy
	}
	for _, m := range out.Meals {
		s.Kcal += m.Nutrients["energy.energy"]
		s.Protein += m.Nutrients["nutrient.protein"]
		s.Fat += m.Nutrients["nutrient.fat"]
		s.Carb += m.Nutrients["nutrient.carb"]
	}
	if s.KcalGoal <= 0 {
		return Summary{}, errors.New("Yazio: в итоге дня нет цели по калориям")
	}
	return s, nil
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
