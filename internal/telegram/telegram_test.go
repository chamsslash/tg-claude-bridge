package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendSplitsLongText(t *testing.T) {
	var (
		mu    sync.Mutex
		chats []int64
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&v)
		mu.Lock()
		chats = append(chats, v.ChatID)
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("tok", time.Second)
	c.base = srv.URL
	text := strings.Repeat(strings.Repeat("я", 99)+"\n", 50) // 5000 символов
	if err := c.Send(context.Background(), 42, text); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chats) != 2 {
		t.Fatalf("ожидал 2 вызова sendMessage, получил %d", len(chats))
	}
	for i := range chats {
		if chats[i] != 42 || paths[i] != "/bottok/sendMessage" {
			t.Errorf("вызов %d: chat=%d path=%s", i, chats[i], paths[i])
		}
	}
}

// Ошибка сети Go печатает полный URL запроса, а в URL Bot API лежит токен.
// Такая ошибка уходит в логи, которые копируют при отладке.
func TestErrorDoesNotLeakToken(t *testing.T) {
	c := New("123:SECRET-TOKEN", time.Second)
	c.base = "http://127.0.0.1:1" // порт, на котором никто не слушает
	err := c.Send(context.Background(), 1, "привет")
	if err == nil {
		t.Fatal("ожидал сетевую ошибку")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("токен бота в тексте ошибки: %v", err)
	}
	if _, err := c.GetUpdates(context.Background(), 0); err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("GetUpdates: ошибка %v", err)
	}
}

// recorder — Bot API, который запоминает вызовы.
type recorder struct {
	mu    sync.Mutex
	calls []map[string]any
	paths []string
	reply string
}

func (rc *recorder) server(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		json.NewDecoder(r.Body).Decode(&v)
		rc.mu.Lock()
		rc.calls = append(rc.calls, v)
		rc.paths = append(rc.paths, r.URL.Path)
		reply := rc.reply
		rc.mu.Unlock()
		if reply == "" {
			reply = `{"ok":true,"result":{}}`
		}
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	c := New("tok", time.Second)
	c.base = srv.URL
	return c
}

// Клавиатура — только у последней части: у Telegram она одна на сообщение,
// и на первой из нескольких частей её не видно.
func TestSendMarkup(t *testing.T) {
	rc := &recorder{}
	c := rc.server(t)
	kb := Inline([][]Button{{{Text: "Ещё", Data: "more"}}})
	text := strings.Repeat(strings.Repeat("я", 99)+"\n", 50)
	if err := c.SendMarkup(context.Background(), 42, text, kb); err != nil {
		t.Fatal(err)
	}
	if len(rc.calls) != 2 || rc.calls[0]["reply_markup"] != nil {
		t.Fatalf("вызовы %v", rc.calls)
	}
	mk, _ := rc.calls[1]["reply_markup"].(map[string]any)
	rows, _ := mk["inline_keyboard"].([]any)
	if len(rows) != 1 {
		t.Fatalf("reply_markup %v", rc.calls[1]["reply_markup"])
	}
	btn := rows[0].([]any)[0].(map[string]any)
	if btn["text"] != "Ещё" || btn["callback_data"] != "more" {
		t.Fatalf("кнопка %v", btn)
	}
}

func TestReplyKeyboard(t *testing.T) {
	rc := &recorder{}
	c := rc.server(t)
	if err := c.SendMarkup(context.Background(), 42, "меню", Keyboard([][]string{{"А", "Б"}, {"В"}})); err != nil {
		t.Fatal(err)
	}
	mk, _ := rc.calls[0]["reply_markup"].(map[string]any)
	rows, _ := mk["keyboard"].([]any)
	if len(rows) != 2 || mk["resize_keyboard"] != true || mk["is_persistent"] != true {
		t.Fatalf("клавиатура %v", mk)
	}
	if b := rows[0].([]any)[1].(map[string]any); b["text"] != "Б" {
		t.Fatalf("кнопка %v", b)
	}
}

func TestCallbackUpdate(t *testing.T) {
	rc := &recorder{reply: `{"ok":true,"result":[{"update_id":7,"callback_query":{"id":"cb1","data":"week","message":{"chat":{"id":42},"text":"Ещё"}}}]}`}
	c := rc.server(t)
	ups, err := c.GetUpdates(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 1 || ups[0].Callback == nil || ups[0].Callback.ID != "cb1" || ups[0].Callback.Data != "week" ||
		ups[0].Callback.Message == nil || ups[0].Callback.Message.Chat.ID != 42 {
		t.Fatalf("апдейт %+v", ups)
	}
	allowed, _ := rc.calls[0]["allowed_updates"].([]any)
	found := false
	for _, a := range allowed {
		found = found || a == "callback_query"
	}
	if !found {
		t.Fatalf("allowed_updates %v: без callback_query нажатия не придут", allowed)
	}
}

func TestAnswerCallback(t *testing.T) {
	rc := &recorder{}
	c := rc.server(t)
	if err := c.AnswerCallback(context.Background(), "cb1"); err != nil {
		t.Fatal(err)
	}
	if rc.paths[0] != "/bottok/answerCallbackQuery" || rc.calls[0]["callback_query_id"] != "cb1" {
		t.Fatalf("%s %v", rc.paths[0], rc.calls[0])
	}
}

func TestTyping(t *testing.T) {
	rc := &recorder{}
	c := rc.server(t)
	if err := c.Typing(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if rc.paths[0] != "/bottok/sendChatAction" || rc.calls[0]["action"] != "typing" || rc.calls[0]["chat_id"] != float64(42) {
		t.Fatalf("%s %v", rc.paths[0], rc.calls[0])
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bottok/getFile":
			var v struct {
				FileID string `json:"file_id"`
			}
			json.NewDecoder(r.Body).Decode(&v)
			if v.FileID != "f1" {
				t.Errorf("file_id %q", v.FileID)
			}
			w.Write([]byte(`{"ok":true,"result":{"file_path":"photos/a.jpg","file_size":5}}`))
		case "/file/bottok/photos/a.jpg":
			w.Write([]byte("jpeg!"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New("tok", time.Second)
	c.base = srv.URL
	raw, err := c.Download(context.Background(), "f1", 10)
	if err != nil || string(raw) != "jpeg!" {
		t.Fatalf("%q %v", raw, err)
	}
	if _, err := c.Download(context.Background(), "f1", 4); err == nil {
		t.Fatal("файл больше лимита должен отклоняться")
	}
}

func TestDownloadErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":{"file_path":"a.jpg","file_size":1}}`))
	}))
	defer srv.Close()
	c := New("123:SECRET-TOKEN", time.Second)
	c.base = srv.URL
	// Сам файл — на адресе, где никто не слушает: скачивание падает сетевой
	// ошибкой, а она печатает URL с токеном.
	c.http.Transport = &rewrite{to: "127.0.0.1:1", path: "/file/"}
	_, err := c.Download(context.Background(), "f", 10)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err %v", err)
	}
}

// rewrite отправляет запросы с путём path на адрес to.
type rewrite struct{ to, path string }

func (rw *rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, rw.path) {
		r = r.Clone(r.Context())
		r.URL.Host = rw.to
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestBestPhoto(t *testing.T) {
	m := &Message{Photo: []PhotoSize{{FileID: "s", FileSize: 10}, {FileID: "m", FileSize: 100}, {FileID: "l", FileSize: 1000}}}
	if p, ok := m.BestPhoto(500); !ok || p.FileID != "m" {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := m.BestPhoto(5); ok {
		t.Fatal("все размеры тяжелее лимита")
	}
	if _, ok := (&Message{}).BestPhoto(500); ok {
		t.Fatal("фото нет")
	}
}

func TestPhotoUpdate(t *testing.T) {
	var u Update
	json.Unmarshal([]byte(`{"update_id":1,"edited_message":{"chat":{"id":5},"caption":"обед","photo":[{"file_id":"a","file_size":3}]}}`), &u)
	if !u.IsEdit() || u.Body().Caption != "обед" || len(u.Body().Photo) != 1 {
		t.Fatalf("%+v", u)
	}
}
