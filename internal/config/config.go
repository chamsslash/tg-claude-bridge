// Package config читает настройки процесса из окружения.
package config

import (
	"fmt"
	"strconv"
	"time"
)

// Бот и чат зашиты в код сознательно: проект личный, владелец выбрал удобство
// развёртывания вместо секретности. Переменные окружения по-прежнему
// перекрывают эти значения — на другом боте сервис запускается без пересборки.
const (
	defaultBotToken = "8803539369:AAFrIqdFIcK5Lmu3xyZicSUHylFOH8hT6nE"
	defaultChatID   = 1494256272
)

type Config struct {
	BotToken     string
	ChatID       int64
	RoutineURL   string
	RoutineToken string
	StatePath    string
	PollTimeout  time.Duration
}

// Load собирает конфиг из окружения. Адрес и токен рутины обязательны:
// без них мост молча копил бы команды, которые некому исполнить, а тихий
// дефолт здесь опаснее отказа стартовать.
func Load(getenv func(string) string) (*Config, error) {
	c := &Config{
		BotToken:     or(getenv("TELEGRAM_BOT_TOKEN"), defaultBotToken),
		ChatID:       defaultChatID,
		RoutineURL:   getenv("ROUTINE_URL"),
		RoutineToken: getenv("ROUTINE_TOKEN"),
		StatePath:    or(getenv("STATE_PATH"), "/var/lib/tg-bridge/offset.json"),
	}

	for _, f := range []struct{ name, val string }{
		{"ROUTINE_URL", c.RoutineURL},
		{"ROUTINE_TOKEN", c.RoutineToken},
	} {
		if f.val == "" {
			return nil, fmt.Errorf("не задана обязательная переменная %s", f.name)
		}
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

	return c, nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
