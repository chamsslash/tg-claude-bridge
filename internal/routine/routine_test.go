package routine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWakePassesTextAndToken(t *testing.T) {
	var gotText, gotAuth, gotType, gotVersion, gotBeta string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotVersion = r.Header.Get("anthropic-version")
		gotBeta = r.Header.Get("anthropic-beta")
		body, _ := io.ReadAll(r.Body)
		var v map[string]string
		json.Unmarshal(body, &v)
		gotText = v["text"]
	}))
	defer srv.Close()

	if err := New(srv.URL, "секрет").Wake(context.Background(), "поставь встречу"); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gotText != "поставь встречу" {
		t.Errorf("текст команды не доехал: %q", gotText)
	}
	if gotAuth != "Bearer секрет" {
		t.Errorf("не тот заголовок авторизации: %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("не тот Content-Type: %q", gotType)
	}
	// Без этой пары эндпоинт отвечает 400 ещё до проверки токена.
	if gotVersion != apiVersion {
		t.Errorf("не уехал anthropic-version: %q", gotVersion)
	}
	if gotBeta != apiBeta {
		t.Errorf("не уехал anthropic-beta: %q", gotBeta)
	}
}

func TestWakeRetriesServerError(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Первые две попытки падают, третья проходит.
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	c.backoff = time.Millisecond // паузы проверяет не этот тест
	if err := c.Wake(context.Background(), "привет"); err != nil {
		t.Fatalf("после повторов ожидал успех, получил: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("ожидал три попытки, было %d", got)
	}
}

func TestWakeDoesNotRetryClientError(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "bad token")
	}))
	defer srv.Close()

	err := New(srv.URL, "протухший").Wake(context.Background(), "привет")
	if err == nil {
		t.Fatal("ожидал ошибку на 401")
	}
	// Повторять 401 бессмысленно: токен не станет правильным сам собой.
	if got := calls.Load(); got != 1 {
		t.Errorf("ожидал одну попытку, было %d", got)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("ошибка не называет код: %v", err)
	}
}

func TestWakeStopsOnCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := New(srv.URL, "t").Wake(ctx, "привет"); err == nil {
		t.Fatal("на отменённом контексте ожидал ошибку, а не молчаливый успех")
	}
}
