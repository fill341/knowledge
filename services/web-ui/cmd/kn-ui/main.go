// Command kn-ui serves the Knowledge web UI.
//
// Environment: KN_API_URL (default http://127.0.0.1:8787), KN_UI_ADDR (default 127.0.0.1:8790),
// KN_UI_SESSION_KEY or KN_UI_SESSION_KEY_FILE (cookie encryption key), KN_UI_STATIC_DIR
// (serve the frontend from disk instead of the embedded build — for development).
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"kn.local/webui/internal/server"
	"kn.local/webui/internal/session"
	"kn.local/webui/internal/static"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("kn-ui", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	key, err := sessionKey()
	if err != nil {
		return err
	}
	sealer, err := session.NewSealer(key)
	if err != nil {
		return err
	}
	var assets fs.FS = static.FS()
	if dir := os.Getenv("KN_UI_STATIC_DIR"); dir != "" {
		assets = os.DirFS(dir)
	}
	srv, err := server.New(envOr("KN_API_URL", "http://127.0.0.1:8787"), sealer, assets, log)
	if err != nil {
		return err
	}
	addr := envOr("KN_UI_ADDR", "127.0.0.1:8790")
	hs := &http.Server{Addr: addr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(c)
	}()
	log.Info("kn-ui listening", "addr", addr)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func sessionKey() ([]byte, error) {
	if k := os.Getenv("KN_UI_SESSION_KEY"); k != "" {
		return []byte(k), nil
	}
	if p := os.Getenv("KN_UI_SESSION_KEY_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read KN_UI_SESSION_KEY_FILE: %w", err)
		}
		return []byte(strings.TrimSpace(string(b))), nil
	}
	return nil, fmt.Errorf("set KN_UI_SESSION_KEY_FILE (make secrets creates deploy/secrets/ui_session_key)")
}

func healthcheck() int {
	addr := envOr("KN_UI_ADDR", "127.0.0.1:8790")
	_, port, _ := strings.Cut(addr, ":")
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		return 1
	}
	return 0
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
