package gmailwatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/google"
	"github.com/chamsslash/tg-claude-bridge/internal/llm"
	"github.com/chamsslash/tg-claude-bridge/internal/state"
)

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type fakeGmail struct {
	ids     []string // самые новые первыми
	queries []string
	err     error
}

func (g *fakeGmail) GmailList(_ context.Context, q string, max int) ([]string, error) {
	g.queries = append(g.queries, q)
	if g.err != nil {
		return nil, g.err
	}
	return g.ids[:min(max, len(g.ids))], nil
}

func (g *fakeGmail) GmailGet(_ context.Context, id string) (*google.Letter, error) {
	return &google.Letter{ID: id, From: "от " + id, Subject: "тема " + id, Date: t0, Body: "тело " + id}, nil
}

type sent struct{ texts []string }

func (s *sent) Send(_ context.Context, _ int64, text string) error {
	s.texts = append(s.texts, text)
	return nil
}

// claude отвечает: письмо с id, начинающимся на «ad», — рассылка.
func claude(t *testing.T, down bool) *llm.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			w.WriteHeader(502)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		var items []string
		for n := 1; strings.Contains(body, fmt.Sprintf(`n=\"%d\"`, n)); n++ {
			kind := "main"
			// Тема письма n стоит сразу после его тега.
			i := strings.Index(body, fmt.Sprintf(`n=\"%d\"`, n))
			if strings.Contains(body[i:i+80], "тема ad") {
				kind = "other"
			}
			items = append(items, fmt.Sprintf(`{"n":%d,"kind":"%s","summary":"суть %d"}`, n, kind, n))
		}
		io.WriteString(w, `{"result":"","structured":{"items":[`+strings.Join(items, ",")+`]}}`)
	}))
	t.Cleanup(srv.Close)
	return llm.New(srv.URL, llm.NewBudget(100, time.Hour, time.Now), time.Minute)
}

type fixture struct {
	g  *fakeGmail
	s  *sent
	st *state.Store
	w  *Watcher
	t  time.Time
}

func newFixture(t *testing.T, down bool) *fixture {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{g: &fakeGmail{}, s: &sent{}, st: st, t: t0}
	f.w = New(f.g, claude(t, down), st, f.s, 1, func() time.Time { return f.t })
	return f
}

func TestPollSendsOnceAndMarksSeen(t *testing.T) {
	f := newFixture(t, false)
	f.g.ids = []string{"b", "a"}
	if err := f.w.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.s.texts) != 1 || !strings.Contains(f.s.texts[0], "1 · от a") || !strings.Contains(f.s.texts[0], "2 · от b") {
		t.Fatalf("сводка %q", f.s.texts)
	}
	f.t = f.t.Add(time.Hour)
	f.w.Poll(context.Background())
	if len(f.s.texts) != 1 {
		t.Fatalf("те же письма ушли повторно: %q", f.s.texts)
	}
	if !strings.Contains(f.g.queries[1], fmt.Sprint(t0.Add(-overlap).Unix())) {
		t.Fatalf("второй запрос должен идти от прошлой сводки: %q", f.g.queries[1])
	}
}

func TestPollAllNoiseSilent(t *testing.T) {
	f := newFixture(t, false)
	f.g.ids = []string{"ad1", "ad2"}
	if err := f.w.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.s.texts) != 0 {
		t.Fatalf("одни рассылки — плановая сводка молчит: %q", f.s.texts)
	}
	if len(f.st.Get().Gmail.Seen) != 2 {
		t.Fatal("отсеянные письма тоже отмечаются, иначе их пересказывали бы каждый час")
	}
}

// Claude лежит — письма не отмечаются и придут, когда он вернётся.
func TestPollClaudeDownRetries(t *testing.T) {
	f := newFixture(t, true)
	f.g.ids = []string{"a"}
	if err := f.w.Poll(context.Background()); err != nil {
		t.Fatalf("сбой Claude — не сбой Gmail: %v", err)
	}
	if len(f.s.texts) != 0 || len(f.st.Get().Gmail.Seen) != 0 || !f.st.Get().Gmail.Since.IsZero() {
		t.Fatal("без пересказа ничего не шлём и не отмечаем")
	}
}

func TestPollOverflowKeepsWindow(t *testing.T) {
	f := newFixture(t, false)
	for i := range maxLetters + 5 {
		f.g.ids = append([]string{fmt.Sprintf("m%02d", i)}, f.g.ids...)
	}
	f.w.Poll(context.Background())
	if !f.st.Get().Gmail.Since.IsZero() {
		t.Fatal("остаток не влез — окно сдвигать нельзя")
	}
	f.w.Poll(context.Background())
	if len(f.s.texts) != 2 || !strings.Contains(f.s.texts[1], "от m24") || strings.Contains(f.s.texts[1], "от m00") {
		t.Fatalf("остаток второй сводкой: %q", f.s.texts)
	}
}

func TestButton(t *testing.T) {
	f := newFixture(t, false)
	f.g.ids = []string{"ad1", "a"}
	got := f.w.Button(context.Background())
	if !strings.Contains(got, "за сутки: 2") || !strings.Contains(got, "от a") || strings.Contains(got, "от ad1") || !strings.HasSuffix(got, "Отсеяно рассылок: 1.") {
		t.Fatalf("ответ %q", got)
	}
	if len(f.st.Get().Gmail.Seen) != 0 {
		t.Fatal("кнопка не трогает плановую сводку")
	}
	if f.g.queries[0] != "in:inbox newer_than:1d" {
		t.Fatalf("запрос %q", f.g.queries[0])
	}
}

func TestButtonNothing(t *testing.T) {
	f := newFixture(t, false)
	f.g.ids = []string{"ad1"}
	if got := f.w.Button(context.Background()); got != "📧 В Gmail за сутки ничего нового.\n\nОтсеяно рассылок: 1." {
		t.Fatalf("ответ %q", got)
	}
}

func TestButtonClaudeDownShowsHeaders(t *testing.T) {
	f := newFixture(t, true)
	f.g.ids = []string{"a"}
	if got := f.w.Button(context.Background()); !strings.Contains(got, "без пересказа") || !strings.Contains(got, "от a") {
		t.Fatalf("ответ %q", got)
	}
}

func TestButtonGmailDown(t *testing.T) {
	f := newFixture(t, false)
	f.g.err = errors.New("503")
	if got := f.w.Button(context.Background()); got != DownText {
		t.Fatalf("ответ %q", got)
	}
}

func TestNotConfigured(t *testing.T) {
	w := New(nil, nil, nil, nil, 0, time.Now)
	if w.Poll(context.Background()) != nil || w.Button(context.Background()) != NotConfiguredText {
		t.Fatal("без Gmail — тишина и подсказка по кнопке")
	}
}
