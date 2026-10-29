// Package summary — сводка писем Gmail: Claude пересказывает, сообщение
// собирает сервис.
//
// Claude читает чужие письма, поэтому от него берутся только пересказ,
// «нужно сделать» и код — и те проверяются. Отправитель, тема, дата и
// вложения идут из данных сервиса: подделать «от кого» инструкция в письме не
// может. Рассылки (kind "other") в сводку не попадают вовсе.
package summary

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chamsslash/tg-claude-bridge/internal/llm"
	"github.com/chamsslash/tg-claude-bridge/internal/msk"
)

// BodyLimit — сколько символов тела письма уходит Claude.
const BodyLimit = 2000

const separator = "━━━━━━━━━━━━━━━━━━━━"

// FallbackNote — Claude недоступен, письма идут заголовками.
const FallbackNote = "⚠️ Claude сейчас недоступен — письма без пересказа."

type Letter struct {
	From        string // как показать отправителя: имя или адрес
	Subject     string
	Date        time.Time
	Body        string
	Attachments []string
}

//go:embed prompt_gmail.txt
var system string

// Summary — готовая сводка.
type Summary struct {
	Text     string
	Main     int  // писем отдельными блоками
	Other    int  // писем в «Прочем» или отсеянных
	Fallback bool // без пересказа: Claude недоступен
}

type item struct {
	N        int    `json:"n"`
	Kind     string `json:"kind"`
	Summary  string `json:"summary"`
	Need     string `json:"need"`
	Code     string `json:"code"`
	CodeNote string `json:"code_note"`
}

type result struct {
	Items []item `json:"items"`
}

var schema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "n": {"type": "integer"},
          "kind": {"type": "string", "enum": ["main", "other"]},
          "summary": {"type": "string"},
          "need": {"type": "string"},
          "code": {"type": "string"},
          "code_note": {"type": "string"}
        },
        "required": ["n", "kind", "summary"]
      }
    }
  },
  "required": ["items"]
}`)

// Build пересказывает письма и собирает сообщение с шапкой header. Ошибка
// Claude не теряет письма: вместе с ней возвращается сводка заголовками, а
// отправлять ли её — решает вызывающий.
func Build(ctx context.Context, c *llm.Client, header string, letters []Letter) (Summary, error) {
	var res result
	err := c.JSON(ctx, system, prompt(letters), schema, &res)
	if err != nil {
		return fallback(header, letters), err
	}
	return assemble(header, letters, res), nil
}

var tagRe = regexp.MustCompile(`(?i)<\s*/?\s*письмо`)

// clean убирает из чужого текста теги обёртки: письмо не должно уметь
// «закрыть» себя и продолжить промпт от лица сервиса.
func clean(s string) string {
	return tagRe.ReplaceAllStringFunc(s, func(m string) string { return strings.Replace(m, "<", "‹", 1) })
}

func prompt(letters []Letter) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Сегодня %s по Москве. Писем: %d.\n", time.Now().In(msk.Location).Format("02.01.2006"), len(letters))
	for i, l := range letters {
		fmt.Fprintf(&b, "\n<письмо n=\"%d\">\nОт: %s\nТема: %s\nДата: %s\n", i+1,
			clean(l.From), clean(l.Subject), l.Date.In(msk.Location).Format("02.01.2006 15:04"))
		if len(l.Attachments) > 0 {
			fmt.Fprintf(&b, "Вложения: %s\n", clean(strings.Join(l.Attachments, ", ")))
		}
		fmt.Fprintf(&b, "---\n%s\n</письмо>\n", clean(truncate(l.Body, BodyLimit)))
	}
	return b.String()
}

func assemble(header string, letters []Letter, res result) Summary {
	byN := map[int]item{}
	for _, it := range res.Items {
		if it.N >= 1 && it.N <= len(letters) {
			if _, dup := byN[it.N]; !dup {
				byN[it.N] = it
			}
		}
	}

	var blocks []string
	other := 0
	for i, l := range letters {
		it, ok := byN[i+1]
		// Письмо без ответа Claude считается рассылкой: без пересказа в
		// Gmail его не отличить от рекламы.
		if !ok || it.Kind != "main" || strings.TrimSpace(it.Summary) == "" {
			other++
			continue
		}
		blocks = append(blocks, block(len(blocks)+1, l, it))
	}

	s := Summary{Main: len(blocks), Other: other}
	s.Text = header + "\n\n" + strings.Join(blocks, "\n\n")
	return s
}

func block(n int, l Letter, it item) string {
	var b strings.Builder
	b.WriteString(separator + "\n")
	if code := validCode(it.Code, l); code != "" {
		b.WriteString("🔑 Код: " + code)
		if note := line(it.CodeNote, 80); note != "" {
			b.WriteString(" (" + note + ")")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%d · %s\n%s\n%s\n\n%s", n, l.From, subject(l), when(l.Date), text(it.Summary, 700))
	if need := line(it.Need, 300); need != "" {
		b.WriteString("\n\n⚠️ Нужно: " + need)
	}
	if len(l.Attachments) > 0 {
		b.WriteString("\n📎 " + strings.Join(l.Attachments, ", "))
	}
	return b.String()
}

func fallback(header string, letters []Letter) Summary {
	blocks := []string{FallbackNote}
	for i, l := range letters {
		b := fmt.Sprintf("%s\n%d · %s\n%s\n%s", separator, i+1, l.From, subject(l), when(l.Date))
		if len(l.Attachments) > 0 {
			b += "\n📎 " + strings.Join(l.Attachments, ", ")
		}
		blocks = append(blocks, b)
	}
	return Summary{Text: header + "\n\n" + strings.Join(blocks, "\n\n"), Main: len(letters), Fallback: true}
}

func subject(l Letter) string {
	if s := strings.TrimSpace(l.Subject); s != "" {
		return s
	}
	return "без темы"
}

func when(t time.Time) string { return t.In(msk.Location).Format("02.01, 15:04") }

var codeRe = regexp.MustCompile(`^[A-Za-z0-9-]{3,16}$`)

// validCode — код, только если он символ в символ есть в этом письме:
// выдумать код или подсунуть его из другого письма Claude не может.
func validCode(code string, l Letter) string {
	code = strings.TrimSpace(code)
	if !codeRe.MatchString(code) || !strings.Contains(l.Subject+"\n"+l.Body, code) {
		return ""
	}
	return code
}

var (
	linkRe     = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	markdownRe = regexp.MustCompile("\\*\\*|__|`")
	blanksRe   = regexp.MustCompile(`\n{3,}`)
)

// text — пересказ Claude без ссылок и разметки, не длиннее n символов.
// Ссылку Claude мог взять из письма, а мог и подсунуть — открывать ссылки
// стоит из самого письма.
func text(s string, n int) string {
	s = linkRe.ReplaceAllString(s, "[ссылка в письме]")
	s = markdownRe.ReplaceAllString(s, "")
	s = blanksRe.ReplaceAllString(strings.TrimSpace(s), "\n\n")
	return truncate(s, n)
}

// line — text в одну строку.
func line(s string, n int) string {
	return strings.Join(strings.Fields(text(s, n)), " ")
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
