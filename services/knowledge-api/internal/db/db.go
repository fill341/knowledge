// Package db opens the connection pool and applies embedded migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

type Migration struct {
	Version string // file name without extension, e.g. "0001_init"
	SQL     string
}

func Migrations() ([]Migration, error) {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for _, n := range names {
		b, err := migrationsFS.ReadFile(n)
		if err != nil {
			return nil, err
		}
		v := strings.TrimSuffix(strings.TrimPrefix(n, "migrations/"), ".sql")
		out = append(out, Migration{Version: v, SQL: string(b)})
	}
	return out, nil
}

// MigrateUp applies pending migrations, each in its own transaction.
// It returns the versions that were applied.
func MigrateUp(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}
	// Serialize concurrent migrators.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(7346201)`); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(7346201)`)

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	vs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		applied[v] = true
	}

	ms, err := Migrations()
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range ms {
		if applied[m.Version] {
			continue
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// No arguments → simple protocol, so a file may hold many statements.
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version)
			return err
		})
		if err != nil {
			return done, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

// Pending reports migrations not yet applied; used by the readiness probe.
func Pending(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) (int, error) {
	rows, err := q.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return 0, err
	}
	vs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, v := range vs {
		have[v] = true
	}
	ms, err := Migrations()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range ms {
		if !have[m.Version] {
			n++
		}
	}
	return n, nil
}
