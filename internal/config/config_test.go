package config

import "testing"

func full() map[string]string {
	return map[string]string{
		"TELEGRAM_BOT_TOKEN": "123:abc",
		"TELEGRAM_CHAT_ID":   "424242",
		"ROUTINE_URL":        "https://example.test/trigger",
		"ROUTINE_TOKEN":      "secret",
	}
}

func getenv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(getenv(full()))
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if c.ChatID != 424242 {
		t.Errorf("chat id разобран неверно: %d", c.ChatID)
	}
	if c.PollTimeout.Seconds() != 50 {
		t.Errorf("ожидал дефолтные 50s, получил %s", c.PollTimeout)
	}
	if c.StatePath == "" {
		t.Error("путь состояния не должен быть пустым")
	}
}

func TestLoadRequiresEverySecret(t *testing.T) {
	// Мост без ChatID слушал бы команды от кого угодно, а без токена рутины
	// молча копил бы сообщения — тихий дефолт здесь опаснее отказа стартовать.
	for _, missing := range []string{
		"TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID", "ROUTINE_URL", "ROUTINE_TOKEN",
	} {
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
