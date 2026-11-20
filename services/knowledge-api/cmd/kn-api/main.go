// Command kn-api is the Knowledge API service.
//
//	kn-api serve                          run the REST API
//	kn-api migrate up                     apply database migrations (run as the schema owner)
//	kn-api token create --principal me --scope admin
//	                                      mint a token directly in the database (bootstrap)
//	kn-api healthcheck                    probe the local /healthz (for Docker, no shell needed)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"kn.local/api/internal/config"
	"kn.local/api/internal/db"
	"kn.local/api/internal/httpapi"
	"kn.local/api/internal/service"
	"kn.local/api/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx)
	case "migrate":
		err = migrate(ctx, os.Args[2:])
	case "token":
		err = token(ctx, os.Args[2:])
	case "healthcheck":
		err = healthcheck()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: kn-api serve | migrate up | token create --principal NAME --scope SCOPE... | healthcheck")
	os.Exit(2)
}

func logger(level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func serve(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logger(cfg.LogLevel)
	pool, err := db.Open(ctx, cfg.DB.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()

	st := store.New(pool)
	svc := service.New(st)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(svc, log),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go purgeIdempotency(ctx, st, log)
	go snapshotHealth(ctx, svc, log)

	errc := make(chan error, 1)
	go func() {
		log.Info("knowledge-api listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// snapshotHealth stores a daily health snapshot (ADR-13): shortly after start, then every few hours;
// only the first snapshot of a day is kept, so restarts and repeats are harmless.
func snapshotHealth(ctx context.Context, svc *service.Service, log *slog.Logger) {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if saved, err := svc.SnapshotHealth(ctx); err != nil {
			log.Warn("health snapshot failed", "err", err)
		} else if saved {
			log.Info("health snapshot saved")
		}
		timer.Reset(6 * time.Hour)
	}
}

func purgeIdempotency(ctx context.Context, st *store.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := st.Q().PurgeIdempotent(ctx); err != nil {
				log.Warn("purge idempotency keys", "err", err)
			} else if n > 0 {
				log.Info("purged idempotency keys", "count", n)
			}
		}
	}
}

func migrate(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "up" {
		return errors.New("usage: kn-api migrate up")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	conn, err := connectWithRetry(ctx, cfg.DB.DSN())
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	applied, err := db.MigrateUp(ctx, conn)
	for _, v := range applied {
		fmt.Println("applied", v)
	}
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("database is up to date")
	}
	return nil
}

func connectWithRetry(ctx context.Context, dsn string) (*pgx.Conn, error) {
	var lastErr error
	for i := 0; i < 30; i++ {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, lastErr
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func token(ctx context.Context, args []string) error {
	if len(args) < 1 || args[0] != "create" {
		return errors.New("usage: kn-api token create --principal NAME --scope SCOPE [--scope ...] [--name NAME]")
	}
	fs := flag.NewFlagSet("token create", flag.ExitOnError)
	principal := fs.String("principal", "me", "principal name")
	name := fs.String("name", "", "token name (defaults to principal)")
	days := fs.Int("expires-in-days", 0, "expiry in days, 0 = never")
	create := fs.Bool("create-principal", false, "create an agent principal if it does not exist")
	var scopes multiFlag
	fs.Var(&scopes, "scope", "scope: admin or space:<slug>:<read|write|review|admin> (repeatable)")
	_ = fs.Parse(args[1:])

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DB.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)
	var issued service.IssuedToken
	err = st.Tx(ctx, func(q *store.Queries) error {
		var err error
		issued, err = service.IssueToken(ctx, q, service.CreateTokenInput{
			Principal: *principal, Name: *name, Scopes: scopes, ExpiresInDays: *days, CreatePrincipal: *create,
		})
		if err != nil {
			return err
		}
		return q.Audit(ctx, store.AuditEvent{Action: "token.create", TargetType: "token", TargetID: &issued.ID,
			Details: map[string]any{"principal": issued.Principal, "prefix": issued.Prefix, "via": "cli"}})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "token for %s (scopes: %s) — shown once, store it now:\n", issued.Principal, strings.Join(issued.Scopes, " "))
	fmt.Println(issued.Token)
	return nil
}

func healthcheck() error {
	addr := os.Getenv("KN_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}
	_, port, _ := strings.Cut(addr, ":")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
