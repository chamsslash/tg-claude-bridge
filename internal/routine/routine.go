// Package routine дёргает API-триггер облачной рутины Claude.
package routine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	attempts       = 3
	defaultBackoff = 5 * time.Second
)

type Client struct {
	url   string
	token string
	http  *http.Client

	// backoff вынесен в поле, чтобы тесты не отсиживали боевые паузы.
	backoff time.Duration
}

func New(url, token string) *Client {
	return &Client{
		url:     url,
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
		backoff: defaultBackoff,
	}
}

// Wake запускает сессию рутины и передаёт ей текст команды.
//
// Повторяем только сетевые сбои и 5xx: на них смысл повтора есть. На 4xx
// повтор бесполезен — токен не тот или запрос кривой, и долбиться бессмысленно.
func (c *Client) Wake(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt-1) * c.backoff):
			}
		}

		retryable, err := c.post(ctx, body)
		if err == nil {
			return nil
		}
		last = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("после %d попыток: %w", attempts, last)
}

// post возвращает признак «имеет смысл повторить» вместе с ошибкой.
func (c *Client) post(ctx context.Context, body []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return true, err // сеть моргнула
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 {
		io.Copy(io.Discard, resp.Body)
		return false, nil
	}

	// Тело ошибки короткое и полезное для диагностики, но доверять его размеру
	// нельзя: читаем ограниченно.
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return resp.StatusCode >= 500, fmt.Errorf("рутина ответила %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
}
