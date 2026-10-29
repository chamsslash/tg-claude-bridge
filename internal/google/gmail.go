package google

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Letter — письмо Gmail в том виде, что нужен сводке.
type Letter struct {
	ID          string
	From        string
	Subject     string
	Date        time.Time
	Body        string
	Attachments []string
}

// GmailList — id писем по поисковому запросу Gmail (q), самые новые первыми.
func (c *Client) GmailList(ctx context.Context, q string, max int) ([]string, error) {
	u := fmt.Sprintf("%s/messages?q=%s&maxResults=%d", c.gmailBase, url.QueryEscape(q), max)
	var resp struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := c.do(ctx, "GET", u, nil, &resp); err != nil {
		return nil, err
	}
	ids := make([]string, len(resp.Messages))
	for i, m := range resp.Messages {
		ids[i] = m.ID
	}
	return ids, nil
}

type part struct {
	MimeType string `json:"mimeType"`
	Filename string `json:"filename"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []part `json:"parts"`
}

func (p part) header(name string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// GmailGet — письмо целиком: заголовки, текст тела и имена вложений.
func (c *Client) GmailGet(ctx context.Context, id string) (*Letter, error) {
	var resp struct {
		ID           string `json:"id"`
		InternalDate string `json:"internalDate"`
		Payload      part   `json:"payload"`
	}
	if err := c.do(ctx, "GET", c.gmailBase+"/messages/"+url.PathEscape(id)+"?format=full", nil, &resp); err != nil {
		return nil, err
	}
	l := &Letter{ID: resp.ID, From: sender(resp.Payload.header("From")), Subject: resp.Payload.header("Subject")}
	if ms, err := strconv.ParseInt(resp.InternalDate, 10, 64); err == nil {
		l.Date = time.UnixMilli(ms)
	}
	var plain, htm string
	walk(resp.Payload, func(p part) {
		switch {
		case p.Filename != "":
			l.Attachments = append(l.Attachments, p.Filename)
		case p.MimeType == "text/plain" && plain == "":
			plain = decode(p.Body.Data)
		case p.MimeType == "text/html" && htm == "":
			htm = decode(p.Body.Data)
		}
	})
	l.Body = strings.TrimSpace(plain)
	if l.Body == "" {
		l.Body = htmlText(htm)
	}
	return l, nil
}

func walk(p part, f func(part)) {
	f(p)
	for _, c := range p.Parts {
		walk(c, f)
	}
}

// decode — base64url Gmail, с паддингом или без.
func decode(s string) string {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return ""
	}
	return string(b)
}

// sender — имя отправителя, а без имени — адрес.
func sender(raw string) string {
	a, err := mail.ParseAddress(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	if a.Name != "" {
		return a.Name
	}
	return a.Address
}

var blockTags = map[string]bool{"p": true, "div": true, "br": true, "tr": true, "li": true, "h1": true, "h2": true, "h3": true, "table": true}

// htmlText — текст письма без разметки: для писем, у которых нет text/plain.
func htmlText(s string) string {
	if s == "" {
		return ""
	}
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return collapse(b.String())
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "head":
				skip++
			default:
				if blockTags[string(name)] {
					b.WriteString("\n")
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "head":
				skip = max(skip-1, 0)
			default:
				if blockTags[string(name)] {
					b.WriteString("\n")
				}
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		}
	}
}

// collapse — пробелы внутри строк в один, пустые строки — не больше одной подряд.
func collapse(s string) string {
	var out []string
	blank := false
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, ln)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
