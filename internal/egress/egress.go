// Package egress — CONNECT-прокси со списком разрешённых хостов: единственный
// выход контейнера claude наружу.
//
// В списке — конкретные хосты или «*:порт»: любой хост на этом порту, если
// все его адреса публичные. Так Claude с вебом (еда по фото) читает любые
// страницы, но не достаёт ни до соседних контейнеров, ни до сервисов на
// хосте, ни до метаданных облака.
package egress

import (
	"context"
	"errors"
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
	allow  map[string]bool // host:port в нижнем регистре
	any    map[string]bool // порты из «*:порт»
	dial   func(network, addr string) (net.Conn, error)
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// New: allow — «host:port» или «*:port» через запятую, порт по умолчанию 443.
func New(allow string) *Proxy {
	p := &Proxy{allow: map[string]bool{}, any: map[string]bool{},
		dial: func(n, a string) (net.Conn, error) {
			return net.DialTimeout(n, a, dialTimeout)
		},
		lookup: net.DefaultResolver.LookupIPAddr,
	}
	for _, h := range strings.Split(allow, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(h, "443")
		}
		if host, port, _ := net.SplitHostPort(h); host == "*" {
			p.any[port] = true
			continue
		}
		p.allow[h] = true
	}
	return p
}

// Allowed — пустит ли прокси к host:port: хост в списке или порт открыт для
// всех (адреса тогда проверяет target).
func (p *Proxy) Allowed(hostport string) bool {
	hostport = strings.ToLower(hostport)
	if p.allow[hostport] {
		return true
	}
	_, port, err := net.SplitHostPort(hostport)
	return err == nil && p.any[port]
}

var errPrivate = errors.New("хост указывает на непубличный адрес")

// target — куда соединяться. Хост из списка — как есть; остальные —
// по первому адресу, и только если все адреса публичные: иначе имя,
// смотрящее во внутреннюю сеть, открыло бы её наружу. Соединяемся с уже
// проверенным адресом, чтобы DNS не подменил его между проверкой и dial.
func (p *Proxy) target(ctx context.Context, hostport string) (string, error) {
	if p.allow[strings.ToLower(hostport)] {
		return hostport, nil
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return "", err
	}
	var ips []net.IPAddr
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else if ips, err = p.lookup(ctx, host); err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", errors.New("у хоста нет адресов")
	}
	for _, ip := range ips {
		if !public(ip.IP) {
			return "", errPrivate
		}
	}
	return net.JoinHostPort(ips[0].IP.String(), port), nil
}

// cgnat — 100.64.0.0/10: общий адрес провайдера, снаружи недоступен.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// public — адрес в интернете, а не в локальной, частной или служебной сети.
func public(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}

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
	addr, err := p.target(r.Context(), r.Host)
	if errors.Is(err, errPrivate) {
		slog.Warn("не пускаю во внутреннюю сеть", "host", r.Host)
		http.Error(w, "host not allowed", http.StatusForbidden)
		return
	}
	if err != nil {
		slog.Warn("не разрешил имя", "host", r.Host, "err", err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	up, err := p.dial("tcp", addr)
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
