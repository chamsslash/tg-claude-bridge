package google

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const authURL = "https://accounts.google.com/o/oauth2/v2/auth"

// Authorize — разовый вход владельца: открывает согласие Google в браузере и
// возвращает refresh-токен. Desktop-клиент OAuth, redirect на loopback и
// PKCE — так Google советует делать для установленных приложений. Запускать
// там, где есть браузер (на ноутбуке), а токен потом положить в .env сервера.
func Authorize(ctx context.Context, clientID, secret string, out io.Writer) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	redirect := "http://" + ln.Addr().String()

	verifier := random()
	sum := sha256.Sum256([]byte(verifier))
	state := random()

	codes := make(chan string, 1)
	errs := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "чужой ответ", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			fmt.Fprintln(w, "Google отказал: "+q.Get("error"))
			errs <- errors.New("Google отказал: " + q.Get("error"))
			return
		}
		fmt.Fprintln(w, "Готово — вкладку можно закрыть и вернуться в терминал.")
		codes <- q.Get("code")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintf(out, "Открой в браузере и разреши доступ:\n\n%s\n\n", authLink(authURL, clientID, redirect, base64.RawURLEncoding.EncodeToString(sum[:]), state))

	var code string
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errs:
		return "", err
	case code = <-codes:
	}
	return exchange(ctx, http.DefaultClient, tokenURL, url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {secret},
		"redirect_uri":  {redirect},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	})
}

func authLink(base, clientID, redirect, challenge, state string) string {
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {strings.Join(Scopes, " ")},
		// offline + consent — иначе Google не выдаст refresh-токен повторно.
		"access_type":           {"offline"},
		"prompt":                {"consent"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	return base + "?" + q.Encode()
}

func exchange(ctx context.Context, hc *http.Client, u string, form url.Values) (string, error) {
	var tok struct {
		Refresh string `json:"refresh_token"`
		Error   string `json:"error"`
		Desc    string `json:"error_description"`
	}
	if err := postForm(ctx, hc, u, form, &tok); err != nil {
		return "", err
	}
	if tok.Error != "" {
		return "", fmt.Errorf("Google: %s %s", tok.Error, tok.Desc)
	}
	if tok.Refresh == "" {
		return "", errors.New("Google не выдал refresh-токен — отзови доступ приложения на myaccount.google.com/permissions и повтори")
	}
	return tok.Refresh, nil
}

func random() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
