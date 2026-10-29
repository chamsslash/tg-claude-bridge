// Command tg-bridge — личный бот владельца: сводка Gmail, команды Google
// Календаря текстом и еда по фото в Yazio.
//
// Claude — в соседнем контейнере (claude-runner), без инструментов: данные
// собирает и действия выполняет этот процесс, Claude только возвращает текст
// или JSON. Подробно — docs/superpowers/specs/2026-10-29-claude-cli-design.md.
//
// `tg-bridge google-auth` — разовый вход в Google: печатает refresh-токен
// для GOOGLE_REFRESH_TOKEN (запускать там, где есть браузер).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/agenda"
	"github.com/chamsslash/tg-claude-bridge/internal/bot"
	"github.com/chamsslash/tg-claude-bridge/internal/config"
	"github.com/chamsslash/tg-claude-bridge/internal/gmailwatch"
	"github.com/chamsslash/tg-claude-bridge/internal/google"
	"github.com/chamsslash/tg-claude-bridge/internal/health"
	"github.com/chamsslash/tg-claude-bridge/internal/llm"
	"github.com/chamsslash/tg-claude-bridge/internal/meal"
	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/telegram"
	"github.com/chamsslash/tg-claude-bridge/internal/yazio"
)

const (
	// Gmail, пролежавший дольше, заслуживает сообщения владельцу.
	downAfter = 6 * time.Hour
	// Claude — короче: пока он не отвечает, не работает почти ничего.
	claudeDownAfter = 30 * time.Minute
	// Потолок паузы между опросами Gmail при сбоях подряд.
	maxBackoff = 6 * time.Hour
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) > 1 && os.Args[1] == "google-auth" {
		googleAuth()
		return
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("некорректная конфигурация", "err", err)
		os.Exit(1)
	}
	if err := run(cfg); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("мост остановился с ошибкой", "err", err)
		os.Exit(1)
	}
	slog.Info("мост остановлен")
}

func run(cfg *config.Config) error {
	st, err := state.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	tg := telegram.New(cfg.BotToken, cfg.PollTimeout)
	hm := health.New(time.Now, downAfter)
	hm.Threshold(health.Claude, claudeDownAfter)

	claude := llm.New(cfg.ClaudeURL, llm.NewBudget(cfg.LLMPerHour, time.Hour, time.Now), cfg.ClaudeTimeout)
	claude.OnResult(func(err error) {
		// Исчерпанный бюджет — не сбой Claude.
		switch {
		case errors.Is(err, llm.ErrBudget):
		case err != nil:
			hm.Failure(health.Claude)
		default:
			hm.Success(health.Claude)
		}
	})
	if !claude.Enabled() {
		slog.Warn("CLAUDE_URL не задан: сводки, команды календаря и еда по фото выключены")
	}

	// Интерфейсы — nil, а не nil-указатель: так сервисы видят, что их часть
	// не настроена.
	var (
		gmailSrc gmailwatch.Gmail
		calSrc   agenda.Calendar
		diary    meal.Diary
	)
	if cfg.GoogleEnabled() {
		g := google.New(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRefreshToken, cfg.RequestTimeout)
		gmailSrc, calSrc = g, g
	} else {
		slog.Warn("GOOGLE_* не заданы: Gmail и Календарь выключены")
	}
	if cfg.YazioEnabled() {
		diary = yazio.New(cfg.YazioUsername, cfg.YazioPassword, cfg.YazioUserAgent, cfg.RequestTimeout)
	} else {
		slog.Warn("YAZIO_* не заданы: еда по фото выключена")
	}

	gmailWatch := gmailwatch.New(gmailSrc, claude, st, tg, cfg.ChatID, time.Now)
	cal := agenda.New(calSrc, claude, st, time.Now)
	meals := meal.New(diary, claude, st, time.Now)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if gmailWatch.Enabled() {
			pollGmail(ctx, cfg.GmailPollInterval, cfg.RequestTimeout+cfg.ClaudeTimeout, gmailWatch.Poll, hm)
		}
	}()
	go func() {
		defer wg.Done()
		sendAlerts(ctx, hm, tg, cfg.ChatID)
	}()

	err = bot.New(tg, st, cfg.ChatID, gmailWatch, cal, meals).Run(ctx)
	stop()
	wg.Wait()
	return err
}

// googleAuth — `tg-bridge google-auth`: вход в Google и refresh-токен в
// терминал. Конфиг моста ему не нужен — только клиент OAuth.
func googleAuth() {
	id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET")
	if id == "" || secret == "" {
		slog.Error("задай GOOGLE_CLIENT_ID и GOOGLE_CLIENT_SECRET Desktop-клиента OAuth")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tok, err := google.Authorize(ctx, id, secret, os.Stdout)
	if err != nil {
		slog.Error("вход в Google не удался", "err", err)
		os.Exit(1)
	}
	fmt.Printf("Готово. Впиши в .env сервера:\n\nGOOGLE_REFRESH_TOKEN=%s\n", tok)
}

// pollGmail — плановая сводка: сразу на старте и дальше по интервалу. При
// сбоях подряд пауза растёт: долбить лежащий Gmail незачем.
func pollGmail(ctx context.Context, interval, timeout time.Duration, poll func(context.Context) error, hm *health.Monitor) {
	failures := 0
	for {
		pctx, cancel := context.WithTimeout(ctx, timeout)
		err := poll(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures++
			hm.Failure(health.Gmail)
			slog.Warn("сводка Gmail не удалась", "err", err, "failures", failures)
		} else {
			failures = 0
			hm.Success(health.Gmail)
		}
		if !sleep(ctx, backoff(interval, failures)) {
			return
		}
	}
}

// sendAlerts раз в минуту пишет владельцу, если Gmail или Claude легли или
// поднялись: иначе протухший токен выглядел бы просто тишиной.
func sendAlerts(ctx context.Context, hm *health.Monitor, tg *telegram.Client, chat int64) {
	for sleep(ctx, time.Minute) {
		for _, a := range hm.Alerts() {
			if err := tg.Send(ctx, chat, a); err != nil && ctx.Err() == nil {
				slog.Warn("не отправил оповещение о здоровье", "err", err)
			}
		}
	}
}

// backoff — пауза перед следующим опросом: интервал, удваиваемый за каждый
// сбой подряд, но не дольше maxBackoff.
func backoff(interval time.Duration, failures int) time.Duration {
	d := interval
	for range failures {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// sleep ждёт d, но прерывается по отмене контекста. Возвращает false, если
// ждать до конца не дали.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
