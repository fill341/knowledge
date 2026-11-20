// Package store is the only code that talks to PostgreSQL.
//
// Every read that returns knowledge takes a Visibility built from the caller's scope, and every
// query filters by it. There is no unscoped variant, so forgetting the check is a compile error.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"kn.local/api/internal/domain"
)

type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Q returns queries running outside a transaction.
func (s *Store) Q() *Queries { return &Queries{db: s.pool} }

// Tx runs fn in a transaction.
func (s *Store) Tx(ctx context.Context, fn func(q *Queries) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Queries{db: tx})
	})
}

type Queries struct {
	db DBTX
}

// Savepoint runs fn in a nested transaction; a failure rolls back only fn's changes.
func (q *Queries) Savepoint(ctx context.Context, fn func(q *Queries) error) error {
	tx, ok := q.db.(pgx.Tx)
	if !ok {
		return errors.New("savepoint outside of a transaction")
	}
	return pgx.BeginFunc(ctx, tx, func(sub pgx.Tx) error {
		return fn(&Queries{db: sub})
	})
}

// SetChangeNote sets the note recorded in entry_versions by the trigger for this transaction.
func (q *Queries) SetChangeNote(ctx context.Context, note string) error {
	_, err := q.db.Exec(ctx, `SELECT set_config('app.change_note', $1, true)`, note)
	return err
}

// Visibility restricts reads to the spaces a token may read.
type Visibility struct {
	All    bool
	Spaces []string
}

func VisibilityOf(s domain.Scope) Visibility {
	return Visibility{All: s.AllSpaces(), Spaces: s.Readable()}
}

// Narrow intersects visibility with an explicit list of requested spaces.
func (v Visibility) Narrow(spaces []string) Visibility {
	if len(spaces) == 0 {
		return v
	}
	return Visibility{All: false, Spaces: spaces}
}

var ErrNotFound = errors.New("not found")

// builder assembles dynamic WHERE clauses with positional arguments.
type builder struct {
	conds []string
	args  []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return fmt.Sprintf("$%d", len(b.args))
}

func (b *builder) and(format string, args ...any) {
	b.conds = append(b.conds, b.expr(format, args...))
}

// expr binds args to the %s placeholders of an SQL fragment and returns the fragment with $n parameters.
func (b *builder) expr(format string, args ...any) string {
	ph := make([]any, len(args))
	for i, a := range args {
		ph[i] = b.arg(a)
	}
	return fmt.Sprintf(format, ph...)
}

// Expr is an SQL fragment with %s placeholders and the values bound to them as query parameters.
type Expr struct {
	SQL  string
	Args []any
}

// X builds an Expr.
func X(sql string, args ...any) Expr { return Expr{SQL: sql, Args: args} }

func (b *builder) visible(v Visibility) {
	if v.All {
		return
	}
	b.and("s.slug = ANY(%s::text[])", nonNil(v.Spaces))
}

func (b *builder) where() string {
	if len(b.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(b.conds, " AND ")
}

func itoa(n int) string { return strconv.Itoa(n) }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// IsUniqueViolation reports a unique constraint violation, optionally for a specific constraint.
func IsUniqueViolation(err error, constraint string) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23505" {
		return false
	}
	return constraint == "" || pg.ConstraintName == constraint
}

// IsCheckViolation reports CHECK (23514) and foreign key (23503) violations.
func IsConstraintViolation(err error) (*pgconn.PgError, bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23514" || pg.Code == "23503" || pg.Code == "22001") {
		return pg, true
	}
	return nil, false
}
