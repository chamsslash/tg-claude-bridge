// Package bot слушает Telegram-бота владельца и раздаёт сообщения сервисам.
//
// Кнопки внизу — сводка Gmail и календарь на сегодня. Свободный текст —
// команда календаря (её разбирает Claude без инструментов, выполняет
// сервис). Фото — еда для Yazio. Кнопки под ответами («Отменить»,
// «Удалить») ловят те же сервисы, что их нарисовали.
package bot

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/telegram"
)

// Главный экран: постоянные кнопки под полем ввода. Нажатие присылает
// подпись обычным сообщением, по ней и узнаётся действие.
const (
	GmailButton    = "📧 Gmail за сутки"
	CalendarButton = "📆 Календарь"
)

const (
	StartText  = "Кнопки — внизу. Встречу можно поставить текстом: «поставь созвон завтра в 15». Фото еды запишу в Yazio."
	AckText    = "Смотрю…"
	MealAck    = "🍽 Считаю…"
	TextOnly   = "Понимаю текст, кнопки внизу и фото еды."
	AsPhoto    = "Пришли картинку как фото, а не файлом — так я её разберу."
	TooBig     = "Фото слишком большое — пришли его обычным фото, без «как файл»."
	PhotoError = "⚠️ Не скачал фото из Telegram — пришли ещё раз."
)

// maxPhoto — потолок фото для Claude: картинки тяжелее 5 МБ он не принимает.
const maxPhoto = 5 << 20

// Пауза после сбоя опроса. Telegram отвечает 409, когда того же бота
// опрашивает кто-то ещё; починить это можно только остановкой второго
// потребителя, поэтому здесь просто ждём и пробуем снова.
const retryPause = 5 * time.Second

type Updater interface {
	GetUpdates(ctx context.Context, offset int) ([]telegram.Update, error)
	Send(ctx context.Context, chat int64, text string) error
	SendMarkup(ctx context.Context, chat int64, text string, markup telegram.Markup) error
	AnswerCallback(ctx context.Context, id string) error
	Typing(ctx context.Context, chat int64) error
	Download(ctx context.Context, fileID string, max int64) ([]byte, error)
}

// Mail — сводка Gmail по кнопке (gmailwatch.Watcher).
type Mail interface {
	Button(ctx context.Context) string
}

// Calendar — календарь (agenda.Agenda): кнопка, команды текстом и кнопки
// под их ответами.
type Calendar interface {
	Today(ctx context.Context) string
	Command(ctx context.Context, text string) (string, telegram.Markup)
	Presser
}

// Meals — еда по фото (meal.Meal).
type Meals interface {
	Photo(ctx context.Context, img []byte, caption string) (string, telegram.Markup)
	Presser
}

// Presser — кнопки под ответами. false — не его кнопка.
type Presser interface {
	Press(ctx context.Context, data string) (string, telegram.Markup, bool)
}

type Bot struct {
	tg    Updater
	st    *state.Store
	chat  int64
	mail  Mail
	cal   Calendar
	meals Meals
}

func New(tg Updater, st *state.Store, chat int64, mail Mail, cal Calendar, meals Meals) *Bot {
	return &Bot{tg: tg, st: st, chat: chat, mail: mail, cal: cal, meals: meals}
}

// Main — главный экран.
func Main() telegram.Markup {
	return telegram.Keyboard([][]string{{GmailButton, CalendarButton}})
}

func (b *Bot) Run(ctx context.Context) error {
	offset := b.st.Get().TelegramOffset
	slog.Info("слушаю бота", "chat", b.chat, "offset", offset)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		updates, err := b.tg.GetUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // это не сбой, а остановка по сигналу
			}
			slog.Warn("опрос Telegram не удался", "err", err)
			if !sleep(ctx, retryPause) {
				return ctx.Err()
			}
			continue
		}

		for _, u := range updates {
			b.handle(ctx, u)
			// Остановили посреди обработки — offset не двигаем: после рестарта
			// Telegram отдаст это сообщение снова.
			if err := ctx.Err(); err != nil {
				return err
			}
			// Offset двигаем после обработки: падение посреди работы означает
			// повтор сообщения, а не его потерю.
			offset = u.UpdateID + 1
			if err := b.st.Update(func(s *state.State) { s.TelegramOffset = offset }); err != nil {
				slog.Error("не сохранил offset", "err", err)
			}
		}
	}
}

func (b *Bot) handle(ctx context.Context, u telegram.Update) {
	if u.Callback != nil {
		b.press(ctx, u.Callback)
		return
	}
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

	switch {
	case len(msg.Photo) > 0:
		// Правка подписи к фото — не новая еда: запись уже в дневнике.
		if u.IsEdit() {
			slog.Info("правка фото, не записываю повторно")
			return
		}
		b.photo(ctx, msg)
		return
	case msg.Document != nil && strings.HasPrefix(msg.Document.MimeType, "image/"):
		b.say(ctx, AsPhoto)
		return
	}

	text := strings.TrimSpace(msg.Text)
	switch text {
	case "":
		b.sayMarkup(ctx, TextOnly, Main())
	case "/start":
		b.sayMarkup(ctx, StartText, Main())
	case GmailButton, "/gmail":
		b.say(ctx, AckText)
		b.sayMarkup(ctx, b.mail.Button(ctx), Main())
	case CalendarButton, "/today":
		b.sayMarkup(ctx, b.cal.Today(ctx), Main())
	default:
		slog.Info("команда владельца", "len", len(text))
		b.typing(ctx)
		reply, mk := b.cal.Command(ctx, text)
		b.reply(ctx, reply, mk)
	}
}

// photo — фото еды: скачать самый крупный размер и отдать meal.
func (b *Bot) photo(ctx context.Context, msg *telegram.Message) {
	p, ok := msg.BestPhoto(maxPhoto)
	if !ok {
		b.say(ctx, TooBig)
		return
	}
	slog.Info("фото еды", "width", p.Width, "height", p.Height, "caption", msg.Caption != "")
	b.say(ctx, MealAck)
	img, err := b.tg.Download(ctx, p.FileID, maxPhoto)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("не скачал фото", "err", err)
			b.say(ctx, PhotoError)
		}
		return
	}
	b.typing(ctx)
	reply, mk := b.meals.Photo(ctx, img, msg.Caption)
	b.reply(ctx, reply, mk)
}

// press — нажатие кнопки под сообщением.
func (b *Bot) press(ctx context.Context, cq *telegram.CallbackQuery) {
	if cq.Message == nil || cq.Message.Chat.ID != b.chat {
		slog.Info("чужое нажатие, игнорирую")
		return
	}
	// Часики на кнопке снимаем сразу: ответ может занять секунды.
	if err := b.tg.AnswerCallback(ctx, cq.ID); err != nil && ctx.Err() == nil {
		slog.Warn("не ответил на нажатие", "err", err)
	}
	slog.Info("кнопка", "data", cq.Data)
	for _, p := range []Presser{b.cal, b.meals} {
		if reply, mk, ok := p.Press(ctx, cq.Data); ok {
			b.reply(ctx, reply, mk)
			return
		}
	}
}

// reply — ответ с кнопками под ним, а без них — с главным экраном.
func (b *Bot) reply(ctx context.Context, text string, mk telegram.Markup) {
	if mk == nil {
		mk = Main()
	}
	b.sayMarkup(ctx, text, mk)
}

func (b *Bot) typing(ctx context.Context) {
	if err := b.tg.Typing(ctx, b.chat); err != nil && ctx.Err() == nil {
		slog.Warn("не показал «печатает»", "err", err)
	}
}

// say отвечает владельцу. Сбой здесь только логируем: это сопроводительное
// сообщение, а не сама операция, и ронять из-за него обработку незачем.
func (b *Bot) say(ctx context.Context, text string) {
	if err := b.tg.Send(ctx, b.chat, text); err != nil && ctx.Err() == nil {
		slog.Warn("не ответил в Telegram", "err", err)
	}
}

// sayMarkup — say с клавиатурой. Главный экран прикладываем к ответам: так
// он появляется и после переустановки Telegram.
func (b *Bot) sayMarkup(ctx context.Context, text string, mk telegram.Markup) {
	if err := b.tg.SendMarkup(ctx, b.chat, text, mk); err != nil && ctx.Err() == nil {
		slog.Warn("не ответил в Telegram", "err", err)
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
