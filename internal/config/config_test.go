package config

import "testing"

func full() map[string]string {
	return map[string]string{
		"ROUTINE_URL":   "https://example.test/trigger",
		"ROUTINE_TOKEN": "secret",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadFallsBackToBuiltInBot(t *testing.T) {
	// Бот и чат зашиты в код, поэтому минимальная конфигурация — только рутина.
	c, err := Load(getenv(full()))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.BotToken != defaultBotToken {
		t.Errorf("ожидал зашитый токен бота, получил %q", c.BotToken)
	}
	if c.ChatID != defaultChatID {
		t.Errorf("ожидал зашитый chat id, получил %d", c.ChatID)
	}
	if c.PollTimeout.Seconds() != 50 {
		t.Errorf("ожидал дефолтные 50s, получил %s", c.PollTimeout)
	}
	if c.StatePath == "" {
		t.Error("путь состояния не должен быть пустым")
	}
}

func TestEnvOverridesBuiltIns(t *testing.T) {
	// Смысл дефолтов в удобстве, а не в жёсткости: на другом боте сервис
	// должен подниматься без пересборки.
	env := full()
	env["TELEGRAM_BOT_TOKEN"] = "999:zzz"
	env["TELEGRAM_CHAT_ID"] = "424242"

	c, err := Load(getenv(env))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.BotToken != "999:zzz" {
		t.Errorf("окружение не перекрыло токен: %q", c.BotToken)
	}
	if c.ChatID != 424242 {
		t.Errorf("окружение не перекрыло chat id: %d", c.ChatID)
	}
}

func TestLoadRequiresRoutine(t *testing.T) {
	for _, missing := range []string{"ROUTINE_URL", "ROUTINE_TOKEN"} {
		env := full()
		delete(env, missing)
		if _, err := Load(getenv(env)); err == nil {
			t.Errorf("без %s ожидал отказ старта", missing)
		}
	}
}

func TestLoadRejectsNonNumericChatID(t *testing.T) {
	env := full()
	env["TELEGRAM_CHAT_ID"] = "@myself"
	if _, err := Load(getenv(env)); err == nil {
		t.Error("ожидал отказ на нечисловом chat id")
	}
}

func TestLoadRejectsAbsurdPollTimeout(t *testing.T) {
	for _, v := range []string{"0s", "10m", "нисколько"} {
		env := full()
		env["POLL_TIMEOUT"] = v
		if _, err := Load(getenv(env)); err == nil {
			t.Errorf("POLL_TIMEOUT=%s должен отвергаться", v)
		}
	}
}
