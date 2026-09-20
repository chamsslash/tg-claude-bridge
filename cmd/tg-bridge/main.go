// Command tg-bridge слушает Telegram-бота и будит облачную рутину Claude.
//
// Рутина не умеет запускаться чаще раза в час, а команда в чате должна
// отрабатывать сразу. Поэтому длинный опрос живёт здесь, на машине, которая и
// так работает круглосуточно, и дёргает API-триггер рутины по событию.
// Здесь же хранится offset: окружение рутины каждый запуск свежее, и помнить
// позицию в очереди обновлений ему негде.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/config"
	"github.com/chamsslash/tg-claude-bridge/internal/routine"
	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/telegram"
)

// Пауза после сбоя опроса. Telegram отвечает 409, когда того же бота
// опрашивает кто-то ещё; починить это можно только остановкой второго
// потребителя, поэтому здесь мы просто ждём и пробуем снова.
const retryPause = 5 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("некорректная конфигурация", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	b := &bridge{
		tg:    telegram.New(cfg.BotToken, cfg.PollTimeout),
		rt:    routine.New(cfg.RoutineURL, cfg.RoutineToken),
		store: state.New(cfg.StatePath),
		chat:  cfg.ChatID,
	}

	if err := b.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("мост остановился с ошибкой", "err", err)
		os.Exit(1)
	}
	slog.Info("мост остановлен")
}

type bridge struct {
	tg    *telegram.Client
	rt    *routine.Client
	store *state.Store
	chat  int64
}

func (b *bridge) run(ctx context.Context) error {
	offset, err := b.store.Load()
	if err != nil {
		return err
	}
	slog.Info("слушаю", "chat", b.chat, "offset", offset)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		updates, err := b.tg.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // это не сбой, а остановка по сигналу
			}
			slog.Warn("опрос не удался", "err", err)
			if !sleep(ctx, retryPause) {
				return ctx.Err()
			}
			continue
		}

		for _, u := range updates {
			b.handle(ctx, u)
			// Offset двигаем после обработки: падение посреди работы означает
			// повтор команды, а не её потерю.
			offset = u.UpdateID + 1
			if err := b.store.Save(offset); err != nil {
				slog.Error("не сохранил offset", "err", err)
			}
		}
	}
}

func (b *bridge) handle(ctx context.Context, u telegram.Update) {
	msg := u.Body()
	if msg == nil {
		return
	}

	// Боту может написать кто угодно, кто его найдёт. Чужое отбрасываем молча:
	// любой ответ подтвердил бы постороннему, что бот живой.
	if msg.Chat.ID != b.chat {
		slog.Info("чужой чат, игнорирую", "chat", msg.Chat.ID)
		return
	}

	text := msg.Text
	if text == "" {
		b.say(ctx, "Пока понимаю только текст.")
		return
	}

	slog.Info("команда", "len", len(text))
	if err := b.rt.Wake(ctx, text); err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.Error("не разбудил рутину", "err", err)
		b.say(ctx, "Не смог разбудить рутину — попробуй ещё раз через минуту.")
		return
	}
	b.say(ctx, "Принял, работаю…")
}

// say уведомляет пользователя. Сбой здесь только логируем: это сопроводительное
// сообщение, а не сама операция, и ронять из-за него обработку незачем.
func (b *bridge) say(ctx context.Context, text string) {
	if err := b.tg.Send(ctx, b.chat, text); err != nil && ctx.Err() == nil {
		slog.Warn("не ответил в телегу", "err", err)
	}
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
