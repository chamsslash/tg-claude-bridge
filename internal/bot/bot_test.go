package bot

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chamsslash/tg-claude-bridge/internal/state"
	"github.com/chamsslash/tg-claude-bridge/internal/telegram"
)

const owner = 42

type fakeTG struct {
	updates  [][]telegram.Update
	sent     []string
	markups  []telegram.Markup
	answered []string
	files    map[string][]byte
	cancel   context.CancelFunc
}

func (f *fakeTG) GetUpdates(ctx context.Context, offset int) ([]telegram.Update, error) {
	if len(f.updates) == 0 {
		f.cancel()
		return nil, ctx.Err()
	}
	u := f.updates[0]
	f.updates = f.updates[1:]
	return u, nil
}
func (f *fakeTG) Send(_ context.Context, _ int64, text string) error {
	return f.SendMarkup(context.Background(), 0, text, nil)
}
func (f *fakeTG) SendMarkup(_ context.Context, _ int64, text string, mk telegram.Markup) error {
	f.sent = append(f.sent, text)
	f.markups = append(f.markups, mk)
	return nil
}
func (f *fakeTG) AnswerCallback(_ context.Context, id string) error {
	f.answered = append(f.answered, id)
	return nil
}
func (f *fakeTG) Typing(context.Context, int64) error { return nil }
func (f *fakeTG) Download(_ context.Context, id string, max int64) ([]byte, error) {
	b, ok := f.files[id]
	if !ok {
		return nil, errors.New("нет файла")
	}
	return b, nil
}

type fakeServices struct {
	commands []string
	photos   []string
	pressed  []string
}

func (s *fakeServices) Button(context.Context) string { return "сводка" }
func (s *fakeServices) Today(context.Context) string  { return "сегодня" }
func (s *fakeServices) Command(_ context.Context, text string) (string, telegram.Markup) {
	s.commands = append(s.commands, text)
	return "поставил", nil
}

type calPresser struct{ *fakeServices }

func (c calPresser) Press(_ context.Context, data string) (string, telegram.Markup, bool) {
	if !strings.HasPrefix(data, "cal:") {
		return "", nil, false
	}
	c.pressed = append(c.pressed, data)
	return "календарь: " + data, nil, true
}

type mealPresser struct{ *fakeServices }

func (m mealPresser) Photo(_ context.Context, img []byte, caption string) (string, telegram.Markup) {
	m.photos = append(m.photos, string(img)+"|"+caption)
	return "записал", telegram.Inline([][]telegram.Button{{{Text: "↩️ Отменить", Data: "meal:undo:k"}}})
}
func (m mealPresser) Press(_ context.Context, data string) (string, telegram.Markup, bool) {
	if !strings.HasPrefix(data, "meal:") {
		return "", nil, false
	}
	m.pressed = append(m.pressed, data)
	return "еда: " + data, nil, true
}

func run(t *testing.T, updates ...telegram.Update) (*fakeTG, *fakeServices, *state.Store) {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tg := &fakeTG{files: map[string][]byte{"big": []byte("jpeg")}, cancel: cancel}
	for _, u := range updates {
		tg.updates = append(tg.updates, []telegram.Update{u})
	}
	svc := &fakeServices{}
	b := New(tg, st, owner, svc, struct {
		*fakeServices
		calPresser
	}{svc, calPresser{svc}}, mealPresser{svc})
	b.Run(ctx)
	return tg, svc, st
}

func text(id int, chat int64, s string) telegram.Update {
	return telegram.Update{UpdateID: id, Message: &telegram.Message{Text: s, Chat: telegram.Chat{ID: chat}}}
}

func photo(id int, caption string) *telegram.Message {
	return &telegram.Message{Chat: telegram.Chat{ID: owner}, Caption: caption, Photo: []telegram.PhotoSize{
		{FileID: "small", FileSize: 100}, {FileID: "big", FileSize: 1000}}}
}

func TestTextIsCalendarCommand(t *testing.T) {
	tg, svc, st := run(t, text(5, owner, "поставь созвон завтра в 15"))
	if len(svc.commands) != 1 || tg.sent[len(tg.sent)-1] != "поставил" {
		t.Fatalf("%v %v", svc.commands, tg.sent)
	}
	if st.Get().TelegramOffset != 6 {
		t.Fatalf("offset %d", st.Get().TelegramOffset)
	}
}

func TestForeignChatIgnored(t *testing.T) {
	tg, svc, _ := run(t, text(1, 777, "привет"), telegram.Update{UpdateID: 2, Message: &telegram.Message{Chat: telegram.Chat{ID: 777}, Photo: []telegram.PhotoSize{{FileID: "big"}}}})
	if len(tg.sent) != 0 || len(svc.commands) != 0 || len(svc.photos) != 0 {
		t.Fatalf("чужому ответили: %v", tg.sent)
	}
}

func TestButtons(t *testing.T) {
	tg, svc, _ := run(t, text(1, owner, GmailButton), text(2, owner, CalendarButton), text(3, owner, "/start"))
	want := []string{AckText, "сводка", "сегодня", StartText}
	if strings.Join(tg.sent, "|") != strings.Join(want, "|") || len(svc.commands) != 0 {
		t.Fatalf("%v", tg.sent)
	}
}

func TestPhotoGoesToMeal(t *testing.T) {
	tg, svc, _ := run(t, telegram.Update{UpdateID: 1, Message: photo(1, "обед")})
	if len(svc.photos) != 1 || svc.photos[0] != "jpeg|обед" {
		t.Fatalf("%v", svc.photos)
	}
	if tg.sent[0] != MealAck || tg.sent[1] != "записал" || tg.markups[1] == nil {
		t.Fatalf("%v", tg.sent)
	}
}

// Правка подписи к фото не записывает еду второй раз.
func TestEditedPhotoIgnored(t *testing.T) {
	tg, svc, _ := run(t, telegram.Update{UpdateID: 1, Edited: photo(1, "ужин")})
	if len(svc.photos) != 0 || len(tg.sent) != 0 {
		t.Fatalf("%v %v", svc.photos, tg.sent)
	}
}

func TestImageDocumentAsksForPhoto(t *testing.T) {
	tg, svc, _ := run(t, telegram.Update{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: owner},
		Document: &telegram.Document{FileID: "d", MimeType: "image/jpeg"}}})
	if len(tg.sent) != 1 || tg.sent[0] != AsPhoto || len(svc.photos) != 0 {
		t.Fatalf("%v", tg.sent)
	}
}

func TestPhotoTooBig(t *testing.T) {
	tg, svc, _ := run(t, telegram.Update{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: owner},
		Photo: []telegram.PhotoSize{{FileID: "huge", FileSize: maxPhoto + 1}}}})
	if len(tg.sent) != 1 || tg.sent[0] != TooBig || len(svc.photos) != 0 {
		t.Fatalf("%v", tg.sent)
	}
}

func TestDownloadFailure(t *testing.T) {
	tg, svc, _ := run(t, telegram.Update{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: owner},
		Photo: []telegram.PhotoSize{{FileID: "missing", FileSize: 10}}}})
	if tg.sent[len(tg.sent)-1] != PhotoError || len(svc.photos) != 0 {
		t.Fatalf("%v", tg.sent)
	}
}

func TestStickerGetsHint(t *testing.T) {
	tg, _, _ := run(t, telegram.Update{UpdateID: 1, Message: &telegram.Message{Chat: telegram.Chat{ID: owner}}})
	if len(tg.sent) != 1 || tg.sent[0] != TextOnly {
		t.Fatalf("%v", tg.sent)
	}
}

func TestPressRouting(t *testing.T) {
	cb := func(id int, data string, chat int64) telegram.Update {
		return telegram.Update{UpdateID: id, Callback: &telegram.CallbackQuery{ID: data, Data: data,
			Message: &telegram.Message{Chat: telegram.Chat{ID: chat}}}}
	}
	tg, svc, _ := run(t, cb(1, "cal:undo:a", owner), cb(2, "meal:undo:b", owner), cb(3, "meal:undo:c", 777), cb(4, "unknown", owner))
	if strings.Join(svc.pressed, ",") != "cal:undo:a,meal:undo:b" {
		t.Fatalf("нажатия %v", svc.pressed)
	}
	if strings.Join(tg.sent, "|") != "календарь: cal:undo:a|еда: meal:undo:b" {
		t.Fatalf("%v", tg.sent)
	}
	if len(tg.answered) != 3 {
		t.Fatalf("часики сняты у %v", tg.answered)
	}
}
