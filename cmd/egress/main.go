// Command egress — CONNECT-прокси со списком хостов для контейнера claude
// (см. internal/egress).
package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/chamsslash/tg-claude-bridge/internal/egress"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	allow := os.Getenv("EGRESS_ALLOW")
	if allow == "" {
		allow = "api.anthropic.com:443,platform.claude.com:443"
	}
	addr := os.Getenv("EGRESS_ADDR")
	if addr == "" {
		addr = ":3128"
	}
	srv := &http.Server{Addr: addr, Handler: egress.New(allow), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("прокси наружу", "addr", addr, "allow", allow)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("прокси остановился", "err", err)
		os.Exit(1)
	}
}
