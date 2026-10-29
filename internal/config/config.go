// Package config читает настройки процесса из окружения.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Чат зашит в код: это не секрет, а просто адрес получателя.
//
// Токен бота раньше лежал здесь же — и трижды умирал в течение суток после
// пуша: репозиторий публичный, а живой токен из публичной репы забирает кто
// угодно и начинает сам читать очередь бота. Наш опрос получал в ответ
// 409 Conflict. Теперь токен приходит только из окружения.
const defaultChatID = 1494256272

type Config struct {
	BotToken       string
	ChatID         int64
	StatePath      string
	PollTimeout    time.Duration
	RequestTimeout time.Duration

	// ClaudeURL — обёртка claude-runner; пусто — Claude выключен: сводки
	// Gmail, команды календаря и еда по фото не работают.
	ClaudeURL     string
	ClaudeTimeout time.Duration
	LLMPerHour    int

	// Google — все три или ничего; без них Gmail и Календарь выключены.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRefreshToken string
	GmailPollInterval  time.Duration

	// Yazio — оба или ничего; без них еда по фото выключена.
	YazioUsername  string
	YazioPassword  string
	YazioUserAgent string
}

// GoogleEnabled — Gmail и Календарь настроены.
func (c *Config) GoogleEnabled() bool { return c.GoogleRefreshToken != "" }

// YazioEnabled — еда по фото настроена.
func (c *Config) YazioEnabled() bool { return c.YazioUsername != "" }

// Load собирает конфиг из окружения. Без токена бота мост не стартует: он
// молча копил бы команды, которые некому исполнить, а тихий дефолт здесь
// опаснее отказа стартовать.
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		BotToken:  getenv("TELEGRAM_BOT_TOKEN"),
		ChatID:    defaultChatID,
		StatePath: or(getenv("STATE_PATH"), "/data/state.json"),

		ClaudeURL:          getenv("CLAUDE_URL"),
		GoogleClientID:     getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: getenv("GOOGLE_CLIENT_SECRET"),
		GoogleRefreshToken: getenv("GOOGLE_REFRESH_TOKEN"),
		YazioUsername:      getenv("YAZIO_USERNAME"),
		YazioPassword:      getenv("YAZIO_PASSWORD"),
		YazioUserAgent:     getenv("YAZIO_USER_AGENT"),
	}

	if c.BotToken == "" {
		return nil, errors.New("не задана обязательная переменная TELEGRAM_BOT_TOKEN")
	}

	if raw := getenv("TELEGRAM_CHAT_ID"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_CHAT_ID=%q — не число", raw)
		}
		c.ChatID = id
	}

	d, err := time.ParseDuration(or(getenv("POLL_TIMEOUT"), "50s"))
	if err != nil {
		return nil, fmt.Errorf("POLL_TIMEOUT: %w", err)
	}
	// Telegram держит длинный опрос не дольше полутора минут, а слишком
	// короткий таймаут превращает длинный опрос в обычное частое дёрганье.
	if d < time.Second || d > 90*time.Second {
		return nil, fmt.Errorf("POLL_TIMEOUT=%s вне разумного диапазона 1s..90s", d)
	}
	c.PollTimeout = d

	// Минута — на пачку писем Gmail: список и до двадцати писем по одному.
	if c.RequestTimeout, err = time.ParseDuration(or(getenv("REQUEST_TIMEOUT"), "1m")); err != nil {
		return nil, fmt.Errorf("REQUEST_TIMEOUT: %w", err)
	}

	if c.ClaudeURL != "" {
		if u, err := url.Parse(c.ClaudeURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("CLAUDE_URL должен быть адресом обёртки, например http://claude:8090")
		}
	}
	if c.ClaudeTimeout, err = time.ParseDuration(or(getenv("CLAUDE_TIMEOUT"), "8m")); err != nil {
		return nil, fmt.Errorf("CLAUDE_TIMEOUT: %w", err)
	}
	c.LLMPerHour, err = strconv.Atoi(or(getenv("LLM_PER_HOUR"), "40"))
	if err != nil || c.LLMPerHour < 1 {
		return nil, fmt.Errorf("LLM_PER_HOUR=%q — нужно целое больше нуля", getenv("LLM_PER_HOUR"))
	}

	// Половина настроек — почти наверняка опечатка в .env: лучше сказать
	// сразу, чем молча работать без Gmail или Yazio.
	if set := countSet(c.GoogleClientID, c.GoogleClientSecret, c.GoogleRefreshToken); set != 0 && set != 3 {
		return nil, errors.New("GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET и GOOGLE_REFRESH_TOKEN задаются все вместе")
	}
	if set := countSet(c.YazioUsername, c.YazioPassword); set == 1 {
		return nil, errors.New("YAZIO_USERNAME и YAZIO_PASSWORD задаются вместе")
	}

	gi, err := time.ParseDuration(or(getenv("GMAIL_POLL_INTERVAL"), "1h"))
	if err != nil {
		return nil, fmt.Errorf("GMAIL_POLL_INTERVAL: %w", err)
	}
	if gi < 10*time.Minute {
		return nil, fmt.Errorf("GMAIL_POLL_INTERVAL=%s слишком часто: минимум 10m — каждая сводка тратит лимит Claude", gi)
	}
	c.GmailPollInterval = gi

	return c, nil
}

func countSet(vs ...string) int {
	n := 0
	for _, v := range vs {
		if v != "" {
			n++
		}
	}
	return n
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
