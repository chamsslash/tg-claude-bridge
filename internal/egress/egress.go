// Package egress — CONNECT-прокси со списком разрешённых хостов: единственный
// выход контейнера claude наружу.
//
// Claude читает чужие письма, и если инструкция из письма когда-нибудь
// пробьётся сквозь все остальные слои, унести данные ей будет некуда: прокси
// пустит только к Anthropic.
package egress

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const dialTimeout = 10 * time.Second

type Proxy struct {
	allow map[string]bool // host:port в нижнем регистре
	dial  func(network, addr string) (net.Conn, error)
}

// New: allow — «host:port» через запятую, порт по умолчанию 443.
func New(allow string) *Proxy {
	p := &Proxy{allow: map[string]bool{}, dial: func(n, a string) (net.Conn, error) {
		return net.DialTimeout(n, a, dialTimeout)
	}}
	for _, h := range strings.Split(allow, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(h, "443")
		}
		p.allow[h] = true
	}
	return p
}

// Allowed — пустит ли прокси к host:port.
func (p *Proxy) Allowed(hostport string) bool { return p.allow[strings.ToLower(hostport)] }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Только CONNECT: обычный HTTP-прокси пришлось бы разбирать по URL, а
	// Claude ходит только по HTTPS.
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
		return
	}
	if !p.Allowed(r.Host) {
		slog.Warn("не пускаю наружу", "host", r.Host)
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}
	up, err := p.dial("tcp", r.Host)
	if err != nil {
		slog.Warn("не достучался", "host", r.Host, "err", err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer up.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	client, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Байты, которые клиент успел прислать вместе с CONNECT, лежат в буфере.
		io.Copy(up, buf)
		closeWrite(up)
	}()
	go func() {
		defer wg.Done()
		io.Copy(client, up)
		closeWrite(client)
	}()
	wg.Wait()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
