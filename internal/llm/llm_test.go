package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func server(t *testing.T, code int, body string, got *Request) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if got != nil {
			json.Unmarshal(raw, got)
		}
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client(url string) *Client {
	return New(url, NewBudget(10, time.Hour, time.Now), time.Minute)
}

func TestRunPassesRequest(t *testing.T) {
	var got Request
	srv := server(t, 200, `{"result":"ок"}`, &got)
	resp, err := client(srv.URL).Run(context.Background(), Request{System: "с", Prompt: "п", Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil || resp.Result != "ок" {
		t.Fatalf("%v %+v", err, resp)
	}
	if got.System != "с" || got.Prompt != "п" || string(got.Schema) != `{"type":"object"}` {
		t.Fatalf("запрос %+v", got)
	}
}

func TestJSONPrefersStructured(t *testing.T) {
	srv := server(t, 200, `{"result":"{\"n\":1}","structured":{"n":2}}`, nil)
	var v struct{ N int }
	if err := client(srv.URL).JSON(context.Background(), "с", "п", nil, &v); err != nil || v.N != 2 {
		t.Fatalf("%v %+v", err, v)
	}
}

func TestJSONFromText(t *testing.T) {
	srv := server(t, 200, `{"result":"Вот:\n`+"```json"+`\n{\"n\":3}\n`+"```"+`"}`, nil)
	var v struct{ N int }
	if err := client(srv.URL).JSON(context.Background(), "с", "п", nil, &v); err != nil || v.N != 3 {
		t.Fatalf("%v %+v", err, v)
	}
}

func TestJSONMissing(t *testing.T) {
	srv := server(t, 200, `{"result":"не знаю"}`, nil)
	var v struct{ N int }
	if err := client(srv.URL).JSON(context.Background(), "с", "п", nil, &v); err == nil {
		t.Fatal("без JSON должна быть ошибка")
	}
}

func TestRunnerError(t *testing.T) {
	srv := server(t, 502, `{"error":"claude: Not logged in"}`, nil)
	var reported error
	c := client(srv.URL)
	c.OnResult(func(err error) { reported = err })
	_, err := c.Run(context.Background(), Request{System: "с", Prompt: "п"})
	if err == nil || reported == nil {
		t.Fatalf("err %v, reported %v", err, reported)
	}
}

func TestDisabled(t *testing.T) {
	if _, err := client("").Run(context.Background(), Request{}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err %v", err)
	}
	var c *Client
	if c.Enabled() {
		t.Fatal("nil-клиент выключен")
	}
}

func TestBudgetExhausted(t *testing.T) {
	srv := server(t, 200, `{"result":"ок"}`, nil)
	c := New(srv.URL, NewBudget(1, time.Hour, time.Now), time.Minute)
	c.Run(context.Background(), Request{System: "с", Prompt: "п"})
	if _, err := c.Run(context.Background(), Request{System: "с", Prompt: "п"}); !errors.Is(err, ErrBudget) {
		t.Fatalf("err %v", err)
	}
}

func TestDecodePassesImages(t *testing.T) {
	var got Request
	srv := server(t, 200, `{"result":"","structured":{"ok":true}}`, &got)
	var v struct{ OK bool }
	img := Image{MediaType: "image/jpeg", Data: "aGVsbG8="}
	if err := client(srv.URL).Decode(context.Background(), Request{System: "с", Prompt: "п", Images: []Image{img}}, &v); err != nil || !v.OK {
		t.Fatalf("%+v %v", v, err)
	}
	if len(got.Images) != 1 || got.Images[0] != img {
		t.Fatalf("картинка не дошла до обёртки: %+v", got.Images)
	}
}
