// Package runner запускает `claude -p` для сервиса — это обёртка в контейнере
// claude.
//
// Флаги запуска зашиты здесь, а не приходят с запросом: сервис передаёт
// системный промпт, промпт, схему ответа и один переключатель — web. Без
// него у Claude нет ни одного инструмента, и всё, что он может, — вернуть
// текст: так идут сводки Gmail и команды календаря, где Claude читает чужие
// письма и приглашения. С web у Claude только WebSearch и WebFetch — для
// еды по фото, где на входе лишь фото и подпись владельца. Ни команд, ни
// файлов, ни MCP нет ни в одном режиме.
//
// Промпт уходит в CLI одной строкой stream-json, а не простым текстом: только
// так в запуск можно положить картинку (фото еды).
package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MaxBody — потолок запроса: фото из Telegram в base64 с большим запасом.
const MaxBody = 8 << 20

// maxImages — картинок в одном запуске: фото еды — одно.
const maxImages = 4

// mediaTypes — какие картинки принимает Claude.
var mediaTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true}

// maxTurns — без инструментов Claude отвечает за один ход; запас нужен
// структурированному ответу, который CLI может перепросить. С вебом каждый
// поиск и каждая страница — ещё ход.
const (
	maxTurns    = "3"
	maxWebTurns = "40"
)

// webTools — инструменты запуска с web: искать и читать страницы, больше
// ничего.
const webTools = "WebSearch,WebFetch"

var (
	ErrBadRequest = errors.New("пустой промпт, некорректная схема или картинка")
	ErrTimeout    = errors.New("claude не ответил вовремя")
)

type Request struct {
	System string          `json:"system"`
	Prompt string          `json:"prompt"`
	Schema json.RawMessage `json:"schema,omitempty"`
	Images []Image         `json:"images,omitempty"`
	// Web — дать Claude поиск и чтение страниц. Только для запросов без
	// чужого текста: письма и приглашения идут без него.
	Web bool `json:"web,omitempty"`
}

// Image — картинка в base64, идёт в запуск перед текстом промпта.
type Image struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type Response struct {
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured,omitempty"`
}

type Runner struct {
	bin     string
	model   string
	timeout time.Duration
	sem     chan struct{}
}

// New: bin — путь к claude, model — пусто для модели по умолчанию,
// parallel — сколько запусков одновременно (остальные ждут очереди).
func New(bin, model string, timeout time.Duration, parallel int) *Runner {
	return &Runner{bin: bin, model: model, timeout: timeout, sem: make(chan struct{}, max(parallel, 1))}
}

// args — флаги запуска. Порядок групп: изоляция, вывод, промпт.
func (r *Runner) args(req Request, mcpConfig string) []string {
	tools, turns := "", maxTurns
	if req.Web {
		tools, turns = webTools, maxWebTurns
	}
	a := []string{
		"-p",
		// Без инструментов, исполняющих команды и код, без WebFetch (если
		// --tools его не назвал) и без пользовательских и проектных настроек.
		"--restricted",
		// Из встроенных инструментов — ничего или только веб: ни чтения
		// файлов, ни записи.
		"--tools", tools,
		// MCP — только из пустого конфига: никаких серверов вообще.
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		"--no-session-persistence",
		"--max-turns", turns,
		// stream-json на входе требует его и на выходе, а тот — --verbose.
		"--input-format", "stream-json",
		"--output-format", "stream-json", "--verbose",
		"--system-prompt", req.System,
	}
	if req.Web {
		// В -p спросить разрешение не у кого: веб разрешён заранее.
		a = append(a, "--allowedTools", webTools)
	}
	if len(req.Schema) > 0 {
		a = append(a, "--json-schema", string(req.Schema))
	}
	if r.model != "" {
		a = append(a, "--model", r.model)
	}
	return a
}

// input — промпт строкой stream-json: одно сообщение пользователя, сначала
// картинки, потом текст.
func input(req Request) ([]byte, error) {
	content := make([]map[string]any, 0, len(req.Images)+1)
	for _, im := range req.Images {
		content = append(content, map[string]any{
			"type":   "image",
			"source": map[string]string{"type": "base64", "media_type": im.MediaType, "data": im.Data},
		})
	}
	content = append(content, map[string]any{"type": "text", "text": req.Prompt})
	line, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": content},
	})
	return append(line, '\n'), err
}

// cliResult — итоговая строка `claude -p --output-format stream-json`
// (`"type":"result"`).
type cliResult struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	IsError    bool            `json:"is_error"`
	Result     string          `json:"result"`
	Structured json.RawMessage `json:"structured_output"`
	NumTurns   int             `json:"num_turns"`
	CostUSD    float64         `json:"total_cost_usd"`
}

func (r *Runner) Run(ctx context.Context, req Request) (*Response, error) {
	if strings.TrimSpace(req.Prompt) == "" || strings.TrimSpace(req.System) == "" {
		return nil, ErrBadRequest
	}
	if len(req.Schema) > 0 && !json.Valid(req.Schema) {
		return nil, ErrBadRequest
	}
	if len(req.Images) > maxImages {
		return nil, ErrBadRequest
	}
	for _, im := range req.Images {
		if !mediaTypes[im.MediaType] || im.Data == "" {
			return nil, ErrBadRequest
		}
		if _, err := base64.StdEncoding.DecodeString(im.Data); err != nil {
			return nil, ErrBadRequest
		}
	}
	in, err := input(req)
	if err != nil {
		return nil, err
	}

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Рабочий каталог на каждый запуск свой и пустой: Claude нечего в нём
	// найти, а следующему запуску — нечего унаследовать.
	dir, err := os.MkdirTemp("", "run-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	mcp := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(mcp, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, r.args(req, mcp)...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(in)
	// Своя группа процессов: по таймауту убиваем и всё, что claude успел
	// породить, а не только его самого.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, ErrTimeout
	}

	res, ok := result(stdout.Bytes())
	if !ok {
		// Промпт в лог не пишем: в нём чужие письма.
		return nil, fmt.Errorf("claude: %v: %s", runErr, tail(stderr.String()+stdout.String(), 300))
	}
	slog.Info("claude ответил", "subtype", res.Subtype, "turns", res.NumTurns,
		"cost_usd", res.CostUSD, "took", time.Since(start).Round(time.Millisecond).String())
	if res.IsError || runErr != nil {
		return nil, fmt.Errorf("claude: %s: %s", res.Subtype, tail(res.Result, 300))
	}
	return &Response{Result: res.Result, Structured: res.Structured}, nil
}

// result — последняя строка вывода с "type":"result"; остальные строки —
// ход разговора, сервису они не нужны.
func result(out []byte) (cliResult, bool) {
	var res cliResult
	found := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var r cliResult
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Type == "result" {
			res, found = r, true
		}
	}
	return res, found
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return "…" + string(r[len(r)-n:])
	}
	return s
}

// Handler — POST /run и GET /healthz.
func (r *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, hr *http.Request) {
		var req Request
		if err := json.NewDecoder(http.MaxBytesReader(w, hr.Body, MaxBody)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "некорректный запрос")
			return
		}
		resp, err := r.Run(hr.Context(), req)
		switch {
		case errors.Is(err, ErrBadRequest):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrTimeout):
			writeErr(w, http.StatusGatewayTimeout, err.Error())
		case err != nil:
			slog.Warn("запуск claude не удался", "err", err)
			writeErr(w, http.StatusBadGateway, err.Error())
		default:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	})
	return mux
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
