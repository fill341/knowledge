package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"kn.local/api/internal/db"
	"kn.local/api/internal/httpapi"
	"kn.local/api/internal/service"
	"kn.local/api/internal/store"
)

// env is a fresh database (migrated, accessed as the restricted kn_app role) behind an httptest server.
type env struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *service.Service
	store *store.Store
	dsn   string // kn_app DSN of the test database
	owner string // schema owner DSN of the test database, for test-only data surgery
}

// ownerExec runs SQL as the schema owner (e.g. to backdate rows); never available to the app.
func (e *env) ownerExec(sql string, args ...any) {
	e.t.Helper()
	c, err := pgx.Connect(context.Background(), e.owner)
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// newEnv creates a throwaway database. Requires KN_TEST_DB_URL (schema owner on the compose postgres)
// and KN_TEST_APP_PASSWORD; `make test` sets both.
func newEnv(t *testing.T) *env {
	t.Helper()
	ownerURL := os.Getenv("KN_TEST_DB_URL")
	appPassword := os.Getenv("KN_TEST_APP_PASSWORD")
	if ownerURL == "" || appPassword == "" {
		t.Skip("KN_TEST_DB_URL / KN_TEST_APP_PASSWORD not set; run `make up && make test`")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, ownerURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kn_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create db: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), ownerURL)
		if err == nil {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(context.Background())
		}
	})
	if _, err := admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+name+" TO kn_app"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	u, _ := url.Parse(ownerURL)
	u.Path = "/" + name
	owner, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if _, err := owner.Exec(ctx, "GRANT USAGE ON SCHEMA public TO kn_app"); err != nil {
		t.Fatalf("grant schema: %v", err)
	}
	if _, err := db.MigrateUp(ctx, owner); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	owner.Close(ctx)
	ownerDSN := u.String()

	u.User = url.UserPassword("kn_app", appPassword)
	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	st := store.New(pool)
	svc := service.New(st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpapi.New(svc, log))
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, svc: svc, store: st, dsn: u.String(), owner: ownerDSN}
}

// token mints a token directly (like `kn-api token create`).
func (e *env) token(principal string, kind string, scopes ...string) string {
	e.t.Helper()
	ctx := context.Background()
	var tok service.IssuedToken
	err := e.store.Tx(ctx, func(q *store.Queries) error {
		if _, err := q.PrincipalByName(ctx, principal); err != nil {
			if _, err := q.CreatePrincipal(ctx, domainKind(kind), principal, ""); err != nil {
				return err
			}
		}
		var err error
		tok, err = service.IssueToken(ctx, q, service.CreateTokenInput{Principal: principal, Scopes: scopes})
		return err
	})
	if err != nil {
		e.t.Fatalf("issue token: %v", err)
	}
	return tok.Token
}

type resp struct {
	Status int
	Header http.Header
	Body   map[string]any
	Raw    []byte
}

func (r resp) str(path ...string) string {
	v := r.get(path...)
	s, _ := v.(string)
	return s
}

func (r resp) num(path ...string) float64 {
	v := r.get(path...)
	f, _ := v.(float64)
	return f
}

func (r resp) list(path ...string) []any {
	v := r.get(path...)
	l, _ := v.([]any)
	return l
}

func (r resp) get(path ...string) any {
	var cur any = r.Body
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

type req struct {
	method, path string
	body         any
	headers      map[string]string
}

func (e *env) do(token string, r req) resp {
	e.t.Helper()
	var body io.Reader
	if r.body != nil {
		switch b := r.body.(type) {
		case string:
			body = bytes.NewBufferString(b)
		default:
			j, _ := json.Marshal(b)
			body = bytes.NewReader(j)
		}
	}
	hr, _ := http.NewRequest(r.method, e.srv.URL+r.path, body)
	if token != "" {
		hr.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(hr)
	if err != nil {
		e.t.Fatalf("%s %s: %v", r.method, r.path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (e *env) get(token, path string) resp { return e.do(token, req{method: "GET", path: path}) }
func (e *env) post(token, path string, body any) resp {
	return e.do(token, req{method: "POST", path: path, body: body})
}

func expect(t *testing.T, r resp, status int) resp {
	t.Helper()
	if r.Status != status {
		t.Fatalf("expected %d, got %d: %s", status, r.Status, r.Raw)
	}
	return r
}
