package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAllowList(t *testing.T) {
	p := New(" API.anthropic.com , platform.claude.com:443,example.org:8443")
	for host, want := range map[string]bool{
		"api.anthropic.com:443":   true,
		"Api.Anthropic.com:443":   true,
		"api.anthropic.com:80":    false,
		"platform.claude.com:443": true,
		"example.org:8443":        true,
		"evil.com:443":            false,
		"anthropic.com:443":       false,
	} {
		if got := p.Allowed(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}

// connect открывает туннель через прокси и возвращает код ответа и соединение.
func connect(t *testing.T, proxy, target string) (int, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial("tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, c, br
}

func TestTunnelToAllowedHost(t *testing.T) {
	// Апстрим — эхо-сервер: что прислали, то и вернул.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(c, c)
		c.Close()
	}()

	srv := httptest.NewServer(New(ln.Addr().String()))
	defer srv.Close()
	code, c, br := connect(t, srv.Listener.Addr().String(), ln.Addr().String())
	defer c.Close()
	if code != 200 {
		t.Fatalf("CONNECT %d", code)
	}
	fmt.Fprint(c, "ping\n")
	line, _ := br.ReadString('\n')
	if line != "ping\n" {
		t.Fatalf("туннель вернул %q", line)
	}
}

func TestDeniedHost(t *testing.T) {
	srv := httptest.NewServer(New("api.anthropic.com"))
	defer srv.Close()
	code, c, _ := connect(t, srv.Listener.Addr().String(), "evil.com:443")
	c.Close()
	if code != http.StatusForbidden {
		t.Fatalf("CONNECT evil.com: %d, want 403", code)
	}
}

func TestPlainHTTPRefused(t *testing.T) {
	srv := httptest.NewServer(New("api.anthropic.com"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/http://api.anthropic.com/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET через прокси: %d", resp.StatusCode)
	}
}
