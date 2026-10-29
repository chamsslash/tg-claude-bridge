// Command claude-runner — обёртка над `claude -p` в контейнере claude:
// принимает от сервиса промпт по HTTP и возвращает ответ Claude.
//
// В контейнере нет секретов сервиса, а наружу он ходит только через egress.
// Запуски с письмами идут без инструментов, поэтому даже Claude, поддавшийся
// инструкциям из письма, ничего не сделает и никуда не сходит; веб есть
// только у еды. Подробно — docs/superpowers/specs/2026-10-29-claude-cli-design.md.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/runner"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Запуск с вебом — несколько поисков и страниц: минуты, а не секунды.
	timeout, err := time.ParseDuration(or(os.Getenv("RUN_TIMEOUT"), "6m"))
	if err != nil {
		slog.Error("RUN_TIMEOUT", "err", err)
		os.Exit(1)
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		// Не падаем: без токена контейнер полезен для проверки сети, а
		// запуски сами вернут внятную ошибку CLI.
		slog.Warn("CLAUDE_CODE_OAUTH_TOKEN не задан — получи его через claude setup-token")
	}
	r := runner.New(or(os.Getenv("CLAUDE_BIN"), "claude"), os.Getenv("CLAUDE_MODEL"), timeout, 1)
	addr := or(os.Getenv("RUNNER_ADDR"), ":8090")
	srv := &http.Server{Addr: addr, Handler: r.Handler(), ReadHeaderTimeout: 5 * time.Second}
	slog.Info("жду запусков", "addr", addr, "timeout", timeout.String())
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("обёртка остановилась", "err", err)
		os.Exit(1)
	}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
