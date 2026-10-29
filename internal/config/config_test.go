package config

import "testing"

func full() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "111:aaa",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadFallsBackToBuiltInChat(t *testing.T) {
	// Чат зашит в код, токен бота — нет: он приходит только из окружения.
	c, err := Load(getenv(full()))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.BotToken != "111:aaa" {
		t.Errorf("токен бота взят не из окружения: %q", c.BotToken)
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

func TestEnvOverridesBuiltInChat(t *testing.T) {
	// Смысл дефолта в удобстве, а не в жёсткости: на другом чате сервис
	// должен подниматься без пересборки.
	env := full()
	env["TELEGRAM_CHAT_ID"] = "424242"

	c, err := Load(getenv(env))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.ChatID != 424242 {
		t.Errorf("окружение не перекрыло chat id: %d", c.ChatID)
	}
}

func TestLoadRequiresBotToken(t *testing.T) {
	if _, err := Load(getenv(map[string]string{})); err == nil {
		t.Error("без TELEGRAM_BOT_TOKEN ожидал отказ старта")
	}
}

// Google, Yazio и Claude необязательны: без них соответствующее просто
// выключено.
func TestOptionalParts(t *testing.T) {
	c, err := Load(getenv(full()))
	if err != nil {
		t.Fatal(err)
	}
	if c.GoogleEnabled() || c.YazioEnabled() || c.ClaudeURL != "" {
		t.Fatalf("%+v", c)
	}
	if c.LLMPerHour != 40 || c.GmailPollInterval.Hours() != 1 || c.StatePath != "/data/state.json" {
		t.Fatalf("дефолты %+v", c)
	}

	env := full()
	env["CLAUDE_URL"] = "http://claude:8090"
	env["GOOGLE_CLIENT_ID"], env["GOOGLE_CLIENT_SECRET"], env["GOOGLE_REFRESH_TOKEN"] = "id", "s", "r"
	env["YAZIO_USERNAME"], env["YAZIO_PASSWORD"] = "u@x.ru", "pw"
	if c, err = Load(getenv(env)); err != nil || !c.GoogleEnabled() || !c.YazioEnabled() {
		t.Fatalf("%+v %v", c, err)
	}
}

// Половина пары — опечатка в .env, о ней лучше узнать на старте.
func TestHalfConfiguredRejected(t *testing.T) {
	for _, set := range []map[string]string{
		{"GOOGLE_CLIENT_ID": "id"},
		{"GOOGLE_CLIENT_ID": "id", "GOOGLE_CLIENT_SECRET": "s"},
		{"YAZIO_USERNAME": "u"},
		{"YAZIO_PASSWORD": "p"},
		{"CLAUDE_URL": "claude:8090"},
		{"LLM_PER_HOUR": "0"},
		{"GMAIL_POLL_INTERVAL": "1m"},
	} {
		env := full()
		for k, v := range set {
			env[k] = v
		}
		if _, err := Load(getenv(env)); err == nil {
			t.Errorf("%v: ожидал отказ старта", set)
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
