package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/msk"
)

type fakeGoogle struct {
	tokens   atomic.Int32
	tokenErr string
	last     *http.Request
	lastBody string
	handle   func(w http.ResponseWriter, r *http.Request)
}

func newGoogle(t *testing.T, g *fakeGoogle) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			g.tokens.Add(1)
			r.ParseForm()
			if g.tokenErr != "" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"`+g.tokenErr+`"}`)
				return
			}
			if r.Form.Get("refresh_token") != "R" || r.Form.Get("grant_type") != "refresh_token" {
				t.Errorf("форма токена %v", r.Form)
			}
			io.WriteString(w, `{"access_token":"A","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer A" {
			t.Errorf("без токена: %s", r.URL)
		}
		raw, _ := io.ReadAll(r.Body)
		g.last, g.lastBody = r, string(raw)
		g.handle(w, r)
	}))
	t.Cleanup(srv.Close)
	c := New("id", "secret", "R", time.Minute)
	c.tokenURL, c.gmailBase, c.calendarBase = srv.URL+"/token", srv.URL+"/gmail", srv.URL+"/cal"
	return c
}

func TestTokenCached(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"items":[]}`) }}
	c := newGoogle(t, g)
	for range 3 {
		if _, err := c.Events(context.Background(), time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if g.tokens.Load() != 1 {
		t.Fatalf("токен запрашивался %d раз, want 1", g.tokens.Load())
	}
}

func TestRevokedToken(t *testing.T) {
	g := &fakeGoogle{tokenErr: "invalid_grant"}
	c := newGoogle(t, g)
	if _, err := c.GmailList(context.Background(), "in:inbox", 10); !errors.Is(err, ErrAuth) {
		t.Fatalf("err %v, want ErrAuth", err)
	}
}

func b64(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func TestGmailGet(t *testing.T) {
	msg := map[string]any{
		"id": "m1", "internalDate": "1792935720000",
		"payload": map[string]any{
			"mimeType": "multipart/mixed",
			"headers":  []map[string]string{{"name": "From", "value": "Ozon <noreply@ozon.ru>"}, {"name": "Subject", "value": "Заказ"}},
			"parts": []map[string]any{
				{"mimeType": "multipart/alternative", "parts": []map[string]any{
					{"mimeType": "text/plain", "body": map[string]string{"data": b64("Заказ ждёт в пункте выдачи")}},
					{"mimeType": "text/html", "body": map[string]string{"data": b64("<p>html</p>")}},
				}},
				{"mimeType": "application/pdf", "filename": "чек.pdf", "body": map[string]string{"attachmentId": "x"}},
			},
		},
	}
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(msg) }}
	l, err := newGoogle(t, g).GmailGet(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if l.From != "Ozon" || l.Subject != "Заказ" || l.Body != "Заказ ждёт в пункте выдачи" || len(l.Attachments) != 1 || l.Attachments[0] != "чек.pdf" {
		t.Fatalf("%+v", l)
	}
	if !l.Date.Equal(time.UnixMilli(1792935720000)) {
		t.Fatalf("дата %v", l.Date)
	}
	if g.last.URL.Query().Get("format") != "full" {
		t.Fatalf("запрос %s", g.last.URL)
	}
}

func TestGmailHTMLOnly(t *testing.T) {
	msg := map[string]any{"id": "m", "payload": map[string]any{"mimeType": "text/html",
		"headers": []map[string]string{{"name": "From", "value": "a@b.c"}},
		"body":    map[string]string{"data": strings.TrimRight(b64("<html><head><style>x{}</style></head><body><p>Привет,</p><div>мир   и  всё</div><script>evil()</script></body></html>"), "=")}}}
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(msg) }}
	l, err := newGoogle(t, g).GmailGet(context.Background(), "m")
	if err != nil {
		t.Fatal(err)
	}
	if l.Body != "Привет,\n\nмир и всё" || l.From != "a@b.c" {
		t.Fatalf("тело %q, от %q", l.Body, l.From)
	}
}

func TestGmailListQuery(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"messages":[{"id":"a"},{"id":"b"}]}`)
	}}
	ids, err := newGoogle(t, g).GmailList(context.Background(), "in:inbox after:123", 20)
	if err != nil || strings.Join(ids, ",") != "a,b" {
		t.Fatalf("%v %v", ids, err)
	}
	if g.last.URL.Query().Get("q") != "in:inbox after:123" || g.last.URL.Query().Get("maxResults") != "20" {
		t.Fatalf("запрос %s", g.last.URL)
	}
}

// Участников и приглашений сервис не создаёт никогда.
func TestInsertNoInvites(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"e1"}`) }}
	e, err := newGoogle(t, g).Insert(context.Background(), Event{Summary: "Созвон", Attendees: []Person{{Email: "evil@x.y"}},
		Start: &EventTime{DateTime: "2026-10-29T15:00:00+03:00"}, End: &EventTime{DateTime: "2026-10-29T16:00:00+03:00"}})
	if err != nil || e.ID != "e1" {
		t.Fatal(e, err)
	}
	if g.last.Method != "POST" || g.last.URL.Query().Get("sendUpdates") != "none" || strings.Contains(g.lastBody, "attendees") {
		t.Fatalf("%s %s %s", g.last.Method, g.last.URL, g.lastBody)
	}
}

func TestMoveOnlyTimes(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"e1"}`) }}
	_, err := newGoogle(t, g).Move(context.Background(), "e1", EventTime{DateTime: "2026-10-30T10:00:00+03:00"}, EventTime{DateTime: "2026-10-30T11:00:00+03:00"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.Unmarshal([]byte(g.lastBody), &body)
	if g.last.Method != "PATCH" || len(body) != 2 || body["start"] == nil || g.last.URL.Query().Get("sendUpdates") != "none" {
		t.Fatalf("%s %s %s", g.last.Method, g.last.URL, g.lastBody)
	}
}

func TestDeleteNotFound(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }}
	if err := newGoogle(t, g).Delete(context.Background(), "e1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestEventsSkipsCancelled(t *testing.T) {
	g := &fakeGoogle{handle: func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[{"id":"a","status":"confirmed"},{"id":"b","status":"cancelled"}]}`)
	}}
	es, err := newGoogle(t, g).Events(context.Background(), time.Now(), time.Now().Add(time.Hour))
	if err != nil || len(es) != 1 || es[0].ID != "a" {
		t.Fatal(es, err)
	}
	if g.last.URL.Query().Get("singleEvents") != "true" {
		t.Fatalf("запрос %s", g.last.URL)
	}
}

func TestOwn(t *testing.T) {
	for _, c := range []struct {
		e    Event
		want bool
	}{
		{Event{}, true},
		{Event{Organizer: &Person{Self: true}}, true},
		{Event{Organizer: &Person{Self: true}, Attendees: []Person{{Self: true}}}, true},
		{Event{Organizer: &Person{Email: "boss@x.y"}}, false},
		{Event{Organizer: &Person{Self: true}, Attendees: []Person{{Self: true}, {Email: "a@b.c"}}}, false},
	} {
		if got := c.e.Own(); got != c.want {
			t.Errorf("%+v: %v", c.e, got)
		}
	}
}

func TestTimes(t *testing.T) {
	e := Event{Start: &EventTime{Date: "2026-10-29"}, End: &EventTime{Date: "2026-10-30"}}
	s, _, allDay, err := e.Times(msk.Location)
	if err != nil || !allDay || s.Day() != 29 {
		t.Fatal(s, allDay, err)
	}
	e = Event{Start: &EventTime{DateTime: "2026-10-29T12:00:00Z"}, End: &EventTime{DateTime: "2026-10-29T13:00:00Z"}}
	s, _, allDay, err = e.Times(msk.Location)
	if err != nil || allDay || s.Hour() != 15 {
		t.Fatal(s, allDay, err)
	}
}

func TestAuthLinkAndExchange(t *testing.T) {
	link, _ := url.Parse(authLink(authURL, "id", "http://127.0.0.1:5555", "chal", "st"))
	q := link.Query()
	if q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" || !strings.Contains(q.Get("scope"), "gmail.readonly") ||
		strings.Contains(q.Get("scope"), "gmail.send") {
		t.Fatalf("ссылка %s", link)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code_verifier") != "v" || r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("форма %v", r.Form)
		}
		io.WriteString(w, `{"refresh_token":"R1","access_token":"A"}`)
	}))
	defer srv.Close()
	tok, err := exchange(context.Background(), http.DefaultClient, srv.URL, url.Values{"code_verifier": {"v"}, "grant_type": {"authorization_code"}})
	if err != nil || tok != "R1" {
		t.Fatal(tok, err)
	}
}
