package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fake struct {
	dir string
	r   *Runner
}

func newFake(t *testing.T, out string) *fake {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out"), []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DIR", dir)
	bin, _ := filepath.Abs("testdata/fakeclaude.sh")
	return &fake{dir: dir, r: New(bin, "", 5*time.Second, 1)}
}

func (f *fake) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// okOut — вывод stream-json: служебные строки, ход разговора и итог.
const okOut = `{"type":"system","subtype":"init","tools":["StructuredOutput"]}
{"type":"assistant","message":{"content":[{"type":"text","text":"привет"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"привет","num_turns":1}
`

// stdinMessage разбирает то, что обёртка отдала CLI на stdin.
func stdinMessage(t *testing.T, raw string) []map[string]any {
	t.Helper()
	if strings.Count(raw, "\n") != 1 || !strings.HasSuffix(raw, "\n") {
		t.Fatalf("на stdin должна быть ровно одна строка, а пришло %q", raw)
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" {
		t.Fatalf("не сообщение пользователя: %q", raw)
	}
	return msg.Message.Content
}

// Изоляция держится на флагах — они должны уходить всегда, что бы ни прислал
// сервис.
func TestIsolationFlags(t *testing.T) {
	f := newFake(t, okOut)
	if _, err := f.r.Run(context.Background(), Request{System: "сис", Prompt: "письма"}); err != nil {
		t.Fatal(err)
	}
	args := f.read(t, "args")
	for _, want := range []string{"[-p]", "[--restricted]", "[--tools]\n[]\n", "[--strict-mcp-config]",
		"[--no-session-persistence]", "[--input-format]\n[stream-json]", "[--output-format]\n[stream-json]",
		"[--verbose]", "[--system-prompt]\n[сис]"} {
		if !strings.Contains(args, want) {
			t.Errorf("нет %q в аргументах:\n%s", want, args)
		}
	}
	if strings.Contains(args, "--json-schema") {
		t.Error("схемы не было — флага быть не должно")
	}
	if strings.Contains(args, "Web") || strings.Contains(args, "--allowedTools") {
		t.Errorf("без web инструментов быть не должно:\n%s", args)
	}
	content := stdinMessage(t, f.read(t, "stdin"))
	if len(content) != 1 || content[0]["type"] != "text" || content[0]["text"] != "письма" {
		t.Errorf("промпт идёт через stdin текстом, а пришло %v", content)
	}
}

// С web у Claude только поиск и чтение страниц, разрешённые заранее; остальная
// изоляция та же.
func TestWebFlags(t *testing.T) {
	f := newFake(t, okOut)
	if _, err := f.r.Run(context.Background(), Request{System: "сис", Prompt: "фото", Web: true}); err != nil {
		t.Fatal(err)
	}
	args := f.read(t, "args")
	for _, want := range []string{"[--restricted]", "[--tools]\n[WebSearch,WebFetch]\n",
		"[--allowedTools]\n[WebSearch,WebFetch]\n", "[--strict-mcp-config]", "[--max-turns]\n[20]\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("нет %q в аргументах:\n%s", want, args)
		}
	}
}

// MCP-конфиг — файл в рабочем каталоге запуска, и в нём нет ни одного сервера.
func TestEmptyMCPConfigInOwnDir(t *testing.T) {
	f := newFake(t, okOut)
	f.r.Run(context.Background(), Request{System: "с", Prompt: "п"})
	args := strings.Split(f.read(t, "args"), "\n")
	var mcp string
	for i, a := range args {
		if a == "[--mcp-config]" {
			mcp = strings.Trim(args[i+1], "[]")
		}
	}
	pwd := strings.TrimSpace(f.read(t, "pwd"))
	if filepath.Dir(mcp) == "" || !strings.HasSuffix(filepath.Dir(mcp), filepath.Base(pwd)) {
		t.Fatalf("конфиг %q не в рабочем каталоге %q", mcp, pwd)
	}
	if _, err := os.Stat(pwd); !os.IsNotExist(err) {
		t.Fatalf("рабочий каталог %q остался после запуска", pwd)
	}
}

// Картинки идут в то же сообщение перед текстом.
func TestImages(t *testing.T) {
	f := newFake(t, okOut)
	img := Image{MediaType: "image/jpeg", Data: "aGVsbG8="}
	if _, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "что на фото", Images: []Image{img}}); err != nil {
		t.Fatal(err)
	}
	content := stdinMessage(t, f.read(t, "stdin"))
	if len(content) != 2 || content[0]["type"] != "image" || content[1]["text"] != "что на фото" {
		t.Fatalf("содержимое %v", content)
	}
	src, _ := content[0]["source"].(map[string]any)
	if src["type"] != "base64" || src["media_type"] != "image/jpeg" || src["data"] != "aGVsbG8=" {
		t.Fatalf("картинка %v", src)
	}
}

// Итог — последняя строка "type":"result", даже если до неё были другие.
func TestLastResultLine(t *testing.T) {
	f := newFake(t, `{"type":"result","subtype":"success","is_error":false,"result":"старый"}
{"type":"assistant","message":{}}
{"type":"result","subtype":"success","is_error":false,"result":"новый"}`)
	resp, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "п"})
	if err != nil || resp.Result != "новый" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestSchemaAndStructured(t *testing.T) {
	f := newFake(t, `{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"items":[]}}`)
	resp, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "п", Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Structured) != `{"items":[]}` {
		t.Fatalf("structured %s", resp.Structured)
	}
	if !strings.Contains(f.read(t, "args"), "[--json-schema]\n[{\"type\":\"object\"}]") {
		t.Fatal("схема не ушла в --json-schema")
	}
}

func TestErrorResult(t *testing.T) {
	f := newFake(t, `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"лимит"}`)
	if _, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "п"}); err == nil || !strings.Contains(err.Error(), "error_max_turns") {
		t.Fatalf("err %v", err)
	}
}

func TestGarbageOutput(t *testing.T) {
	f := newFake(t, "Not logged in")
	t.Setenv("FAKE_EXIT", "1")
	_, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "п"})
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("err %v", err)
	}
}

func TestTimeoutKills(t *testing.T) {
	f := newFake(t, okOut)
	f.r.timeout = 200 * time.Millisecond
	t.Setenv("FAKE_SLEEP", "10")
	start := time.Now()
	_, err := f.r.Run(context.Background(), Request{System: "с", Prompt: "п"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v, want ErrTimeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("процесс не убит по таймауту")
	}
}

func TestBadRequests(t *testing.T) {
	f := newFake(t, okOut)
	for _, req := range []Request{{System: "с"}, {Prompt: "п"}, {System: "с", Prompt: "п", Schema: json.RawMessage(`{`)},
		{System: "с", Prompt: "п", Images: []Image{{MediaType: "text/html", Data: "aGVsbG8="}}},
		{System: "с", Prompt: "п", Images: []Image{{MediaType: "image/png", Data: "не base64"}}},
		{System: "с", Prompt: "п", Images: []Image{{MediaType: "image/png"}}},
		{System: "с", Prompt: "п", Images: make([]Image, maxImages+1)}} {
		if _, err := f.r.Run(context.Background(), req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%+v: err %v", req, err)
		}
	}
}

func TestHandler(t *testing.T) {
	f := newFake(t, okOut)
	srv := httptest.NewServer(f.r.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/run", "application/json", strings.NewReader(`{"system":"с","prompt":"п"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got Response
	json.NewDecoder(resp.Body).Decode(&got)
	if resp.StatusCode != 200 || got.Result != "привет" {
		t.Fatalf("%d %+v", resp.StatusCode, got)
	}

	resp, _ = http.Post(srv.URL+"/run", "application/json", strings.NewReader(`{"prompt":""}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("пустой промпт: %d", resp.StatusCode)
	}
}
