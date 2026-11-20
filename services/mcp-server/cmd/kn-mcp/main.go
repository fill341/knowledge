// Command kn-mcp is the MCP server for the Knowledge API.
//
// stdio (default): launched by the AI tool; token from KN_TOKEN (scripts/kn-mcp-launch reads it
// from the Keychain). HTTP (--http ADDR): each client sends its own token in Authorization.
//
// Environment: KN_API_URL, KN_TOKEN, KN_AGENT, KN_PROJECT, KN_DEFAULT_SPACE.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"kn.local/kclient"
	"kn.local/mcp/internal/tools"
)

const version = "0.1.0"

func main() {
	httpAddr := flag.String("http", "", "serve streamable HTTP on this address instead of stdio")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := tools.Config{
		Agent:        envOr("KN_AGENT", "mcp-client"),
		Project:      os.Getenv("KN_PROJECT"),
		DefaultSpace: os.Getenv("KN_DEFAULT_SPACE"),
		Version:      version,
	}
	apiURL := envOr("KN_API_URL", kclient.DefaultURL)

	if *httpAddr != "" {
		if err := serveHTTP(ctx, *httpAddr, apiURL, cfg); err != nil {
			log.Fatal(err)
		}
		return
	}

	token := os.Getenv("KN_TOKEN")
	if token == "" {
		log.Fatal("KN_TOKEN is not set (use scripts/kn-mcp-launch to read it from the Keychain)")
	}
	cfg.WorkDir, _ = os.Getwd()
	client := kclient.New(apiURL, token)
	client.UserAgent = "kn-mcp/" + version + " (" + cfg.Agent + ")"
	client.Lang = kclient.EnvLang()
	srv := newServer(cfg, client)
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}

func newServer(cfg tools.Config, client *kclient.Client) *mcp.Server {
	st := tools.NewState(cfg, client)
	srv := mcp.NewServer(&mcp.Implementation{Name: "knowledge", Title: "Knowledge base (kn)", Version: version}, tools.Options(st))
	tools.Register(srv, st)
	return srv
}

// serveHTTP creates a server per session bound to the caller's own token. There is no working
// directory to detect a project from, so clients pass KN_PROJECT-like info via the X-KN-Project header.
func serveHTTP(ctx context.Context, addr, apiURL string, cfg tools.Config) error {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			return nil
		}
		c := cfg
		if p := r.Header.Get("X-KN-Project"); p != "" {
			c.Project = p
		}
		if a := r.Header.Get("X-KN-Agent"); a != "" {
			c.Agent = a
		}
		client := kclient.New(apiURL, token)
		client.UserAgent = "kn-mcp/" + version + " (" + c.Agent + ", http)"
		client.Lang = kclient.EnvLang()
		return newServer(c, client)
	}, nil)

	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	fmt.Fprintf(os.Stderr, "kn-mcp listening on %s (streamable HTTP)\n", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
