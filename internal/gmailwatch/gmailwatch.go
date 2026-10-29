// Package gmailwatch — сводка Gmail: раз в час по новым письмам и по кнопке
// «📧 Gmail» за сутки.
//
// Письма читает сервис (gmail.readonly), пересказывает Claude без
// инструментов, рассылки отбрасываются. Плановая сводка, в которой после
// отсева ничего не осталось, молчит.
package gmailwatch

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/google"
	"github.com/chamsslash/tg-claude-bridge/internal/llm"
	"github.com/chamsslash/tg-claude-bridge/internal/msk"
	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/summary"
)

const (
	// maxLetters — писем в одной сводке; остальное — следующей.
	maxLetters = 20
	// overlap — запас назад к Since: письмо с внутренней датой чуть раньше
	// прошлой сводки не должно проскочить. Повтор отсекает Seen.
	overlap = 10 * time.Minute
	// seenTTL — сколько помнить id прошедших писем.
	seenTTL = 3 * 24 * time.Hour

	NotConfiguredText = "📧 Gmail не настроен — см. README, раздел «Google»."
	DownText          = "⚠️ Gmail сейчас не отвечает — попробуй позже."
)

type Gmail interface {
	GmailList(ctx context.Context, q string, max int) ([]string, error)
	GmailGet(ctx context.Context, id string) (*google.Letter, error)
}

type Sender interface {
	Send(ctx context.Context, chat int64, text string) error
}

type Watcher struct {
	g    Gmail
	c    *llm.Client
	st   *state.Store
	s    Sender
	chat int64
	now  func() time.Time
}

// New: g == nil — Gmail не настроен.
func New(g Gmail, c *llm.Client, st *state.Store, s Sender, chat int64, now func() time.Time) *Watcher {
	return &Watcher{g: g, c: c, st: st, s: s, chat: chat, now: now}
}

func (w *Watcher) Enabled() bool { return w.g != nil }

// Poll — плановая сводка. Ошибка — только за Gmail: по ней судят о его
// здоровье, а здоровье Claude считает llm. Без пересказа письма не
// отмечаются и придут в следующей сводке.
func (w *Watcher) Poll(ctx context.Context) error {
	if !w.Enabled() {
		return nil
	}
	now := w.now()
	gs := w.st.Get().Gmail
	since := gs.Since
	if since.IsZero() {
		since = now.Add(-time.Hour)
	}
	ids, err := w.g.GmailList(ctx, fmt.Sprintf("in:inbox after:%d", since.Add(-overlap).Unix()), 100)
	if err != nil {
		return err
	}
	var fresh []string
	for _, id := range ids {
		if _, ok := gs.Seen[id]; !ok {
			fresh = append(fresh, id)
		}
	}
	// Список — самые новые первыми, а сводку читать удобнее по порядку.
	slices.Reverse(fresh)
	rest := 0
	if len(fresh) > maxLetters {
		rest = len(fresh) - maxLetters
		fresh = fresh[:maxLetters]
	}

	if len(fresh) > 0 {
		letters, err := w.fetch(ctx, fresh)
		if err != nil {
			return err
		}
		s, err := summary.Build(ctx, w.c, header("новых", len(letters), now), letters)
		if err != nil {
			// Без пересказа Gmail не шлём: рассылок в нём много, а заголовками
			// они засыпали бы чат. Придут следующей сводкой.
			slog.Warn("пересказ Gmail не удался, письма подождут", "err", err)
			return nil
		}
		if s.Main > 0 {
			if err := w.s.Send(ctx, w.chat, s.Text); err != nil {
				return err
			}
		}
		slog.Info("сводка Gmail", "letters", len(letters), "shown", s.Main, "dropped", s.Other, "remaining", rest)
	}

	return w.st.Update(func(st *state.State) {
		for _, id := range fresh {
			st.Gmail.Seen[id] = now
		}
		for id, at := range st.Gmail.Seen {
			if now.Sub(at) > seenTTL {
				delete(st.Gmail.Seen, id)
			}
		}
		// Не всё влезло — окно не сдвигаем, остаток придёт следующей сводкой.
		if rest == 0 {
			st.Gmail.Since = now
		}
	})
}

// Button — сводка за сутки по кнопке, без отметок: плановую не трогает.
func (w *Watcher) Button(ctx context.Context) string {
	if !w.Enabled() {
		return NotConfiguredText
	}
	ids, err := w.g.GmailList(ctx, "in:inbox newer_than:1d", maxLetters)
	if err != nil {
		slog.Warn("Gmail по кнопке", "err", err)
		return DownText
	}
	if len(ids) == 0 {
		return "📧 В Gmail за сутки писем нет."
	}
	slices.Reverse(ids)
	letters, err := w.fetch(ctx, ids)
	if err != nil {
		slog.Warn("Gmail по кнопке", "err", err)
		return DownText
	}
	s, err := summary.Build(ctx, w.c, header("за сутки", len(letters), w.now()), letters)
	if err != nil {
		slog.Warn("пересказ Gmail по кнопке не удался, отдаю заголовки", "err", err)
		return s.Text
	}
	text := s.Text
	if s.Main == 0 {
		text = "📧 В Gmail за сутки ничего нового."
	}
	if s.Other > 0 {
		text += fmt.Sprintf("\n\nОтсеяно рассылок: %d.", s.Other)
	}
	return text
}

func (w *Watcher) fetch(ctx context.Context, ids []string) ([]summary.Letter, error) {
	out := make([]summary.Letter, 0, len(ids))
	for _, id := range ids {
		l, err := w.g.GmailGet(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, summary.Letter{From: l.From, Subject: l.Subject, Date: l.Date, Body: l.Body, Attachments: l.Attachments})
	}
	return out, nil
}

func header(what string, n int, now time.Time) string {
	return fmt.Sprintf("📧 Gmail · %s: %d · %s", what, n, now.In(msk.Location).Format("02.01 15:04"))
}
