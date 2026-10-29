// Package telegram — минимальный клиент Bot API: длинный опрос, отправка,
// кнопки и скачивание фото. Только то, что нужно мосту.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"time"
)

// MessageLimit — потолок Telegram на длину одного сообщения.
const MessageLimit = 4096

type Client struct {
	token       string
	pollTimeout time.Duration
	http        *http.Client

	// base вынесен в поле, чтобы тесты ходили в локальный сервер, а не в Telegram.
	base string
}

// New собирает клиент. Таймаут HTTP заведомо больше таймаута опроса: длинный
// опрос по определению висит без ответа до самого конца, и таймаут клиента
// обязан пережить это ожидание, иначе каждый пустой цикл выглядел бы обрывом.
func New(token string, pollTimeout time.Duration) *Client {
	return &Client{
		token:       token,
		pollTimeout: pollTimeout,
		http:        &http.Client{Timeout: pollTimeout + 30*time.Second},
		base:        "https://api.telegram.org",
	}
}

type Chat struct {
	ID int64 `json:"id"`
}

type Message struct {
	Text     string      `json:"text"`
	Chat     Chat        `json:"chat"`
	Caption  string      `json:"caption"`
	Photo    []PhotoSize `json:"photo"`
	Document *Document   `json:"document"`
}

// PhotoSize — один из размеров присланного фото; Telegram отдаёт их от
// меньшего к большему.
type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// Document — файл. Картинку файлом бот не разбирает, но узнаёт, чтобы
// попросить прислать её фото.
type Document struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}

// BestPhoto — самый крупный размер фото, не тяжелее max байт; false — фото
// нет или все размеры тяжелее.
func (m *Message) BestPhoto(max int64) (PhotoSize, bool) {
	for i := len(m.Photo) - 1; i >= 0; i-- {
		if p := m.Photo[i]; p.FileSize <= max {
			return p, true
		}
	}
	return PhotoSize{}, false
}

type Update struct {
	UpdateID int            `json:"update_id"`
	Message  *Message       `json:"message"`
	Edited   *Message       `json:"edited_message"`
	Callback *CallbackQuery `json:"callback_query"`
}

// CallbackQuery — нажатие кнопки под сообщением. Message — сообщение с
// кнопкой: по его чату видно, чьё это нажатие.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message"`
}

// Button — кнопка под сообщением: Data приходит боту в CallbackQuery.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// Markup — клавиатура сообщения: Inline или Keyboard.
type Markup map[string]any

// Inline — кнопки под самим сообщением.
func Inline(rows [][]Button) Markup { return Markup{"inline_keyboard": rows} }

// Keyboard — постоянные кнопки под полем ввода: нажатие присылает их текст
// обычным сообщением.
func Keyboard(rows [][]string) Markup {
	kb := make([][]map[string]string, len(rows))
	for i, row := range rows {
		for _, text := range row {
			kb[i] = append(kb[i], map[string]string{"text": text})
		}
	}
	return Markup{"keyboard": kb, "resize_keyboard": true, "is_persistent": true}
}

// IsEdit — апдейт о правке уже отправленного сообщения.
func (u Update) IsEdit() bool { return u.Message == nil && u.Edited != nil }

// Body возвращает содержательную часть апдейта: правку сообщения обрабатываем
// так же, как новое, — человек поправил опечатку в команде, а не отменил её.
func (u Update) Body() *Message {
	if u.Message != nil {
		return u.Message
	}
	return u.Edited
}

type apiError struct {
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram %d: %s", e.Code, e.Description)
}

func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := c.base + "/bot" + c.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error печатает полный адрес запроса, а в нём токен бота.
		// Ошибка уходит в логи — оставляем только причину.
		var ue *neturl.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("%s: %w", method, ue.Err)
		}
		return fmt.Errorf("%s: запрос не удался", method)
	}
	defer resp.Body.Close()

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("%s: неразборчивый ответ: %w", method, err)
	}
	if !envelope.OK {
		return &apiError{Code: resp.StatusCode, Description: envelope.Description}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

// GetUpdates ждёт новые сообщения. Возвращает пустой срез, если за время
// ожидания ничего не пришло, — это штатный исход, а не ошибка.
func (c *Client) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":  offset,
		"timeout": int(c.pollTimeout.Seconds()),
		// Просим только сообщения и нажатия кнопок: остальные типы апдейтов
		// бот не обрабатывает, и тянуть их значило бы двигать offset по
		// событиям, которые мы молча выбрасываем.
		"allowed_updates": []string{"message", "edited_message", "callback_query"},
	}, &out)
	return out, err
}

// Send отправляет текст, при необходимости разбивая его на несколько
// сообщений: Telegram режет всё длиннее MessageLimit.
func (c *Client) Send(ctx context.Context, chatID int64, text string) error {
	return c.SendMarkup(ctx, chatID, text, nil)
}

// SendMarkup — Send с клавиатурой. Она прикрепляется к последней части:
// у Telegram клавиатура одна на сообщение.
func (c *Client) SendMarkup(ctx context.Context, chatID int64, text string, markup Markup) error {
	parts := Split(text, MessageLimit)
	for i, part := range parts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		msg := map[string]any{
			"chat_id":                  chatID,
			"text":                     part,
			"disable_web_page_preview": true,
		}
		if markup != nil && i == len(parts)-1 {
			msg["reply_markup"] = markup
		}
		err := c.call(ctx, "sendMessage", msg, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

// AnswerCallback снимает часики с нажатой кнопки: без ответа Telegram
// показывает их ещё секунд пятнадцать.
func (c *Client) AnswerCallback(ctx context.Context, id string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id}, nil)
}

// Typing показывает «печатает…»: Claude думает секунды, и без этого
// сообщение выглядит потерявшимся.
func (c *Client) Typing(ctx context.Context, chatID int64) error {
	return c.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
}

// Download скачивает файл по file_id, не больше max байт. Ссылка на файл
// содержит токен бота, поэтому в ошибках её нет.
func (c *Client) Download(ctx context.Context, fileID string, max int64) ([]byte, error) {
	var f struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	if f.Path == "" || f.Size > max {
		return nil, fmt.Errorf("getFile: файл недоступен или больше %d байт", max)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/file/bot"+c.token+"/"+f.Path, nil)
	if err != nil {
		return nil, errors.New("скачивание файла: некорректный путь")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *neturl.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("скачивание файла: %w", ue.Err)
		}
		return nil, errors.New("скачивание файла: запрос не удался")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("скачивание файла: %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("скачивание файла: %w", err)
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("скачивание файла: больше %d байт", max)
	}
	return raw, nil
}
