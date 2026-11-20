// Package httpapi exposes the service over REST v1. See docs/api.md and api/openapi.yaml.
package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"kn.local/api/internal/db"
	"kn.local/api/internal/domain"
	"kn.local/api/internal/service"
	"kn.local/api/internal/store"
)

const (
	maxBodyBytes   = 1 << 20  // 1 MiB for regular requests
	maxImportBytes = 64 << 20 // 64 MiB for /v1/import
)

type Server struct {
	svc *service.Service
	log *slog.Logger
	mux *http.ServeMux
}

func New(svc *service.Service, log *slog.Logger) *Server {
	s := &Server{svc: svc, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	}
}

// handler is an endpoint that returns an error instead of writing it.
type handler func(w http.ResponseWriter, r *http.Request, c service.Caller) error

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("GET /readyz", s.ready)

	route := func(pattern string, h handler) { m.Handle(pattern, s.authed(h)) }

	route("GET /v1/me", s.me)
	route("GET /v1/spaces", s.spaces)
	route("GET /v1/kinds", s.kinds)
	route("GET /v1/stats", s.stats)

	route("GET /v1/projects", s.listProjects)
	route("POST /v1/projects", s.createProject)
	route("POST /v1/projects/resolve", s.resolveProject)
	route("GET /v1/projects/{space}/{slug}", s.getProject)
	route("PATCH /v1/projects/{space}/{slug}", s.patchProject)
	route("GET /v1/projects/{space}/{slug}/skill", s.projectSkill)

	route("GET /v1/entries", s.listEntries)
	route("POST /v1/entries", s.createEntry)
	route("GET /v1/entries/{id}", s.getEntry)
	route("PATCH /v1/entries/{id}", s.patchEntry)
	route("POST /v1/entries/{id}/transition", s.transition)
	route("POST /v1/entries/{id}/promote", s.promote)
	route("GET /v1/entries/{id}/versions", s.versions)
	route("GET /v1/entries/{id}/versions/{n}", s.version)
	route("POST /v1/entries/{id}/revert", s.revert)
	route("GET /v1/entries/{id}/links", s.links)
	route("GET /v1/entries/{id}/link-suggestions", s.linkSuggestions)
	route("GET /v1/entries/{id}/impact", s.impact)
	route("POST /v1/entries/{id}/links", s.addLink)
	route("DELETE /v1/entries/{id}/links/{to}/{rel}", s.deleteLink)
	route("POST /v1/entries/{id}/review", s.review)

	route("POST /v1/search", s.search)
	route("POST /v1/context", s.context)
	route("GET /v1/graph", s.graph)
	route("GET /v1/health", s.healthReport)
	route("GET /v1/health/history", s.healthHistory)
	route("POST /v1/eval", s.eval)

	route("POST /v1/sessions", s.startSession)
	route("POST /v1/sessions/{id}/end", s.endSession)

	route("GET /v1/review-queue", s.reviewQueue)

	route("GET /v1/export", s.export)
	route("POST /v1/import", s.importJSONL)

	route("GET /v1/admin/principals", s.listPrincipals)
	route("POST /v1/admin/principals", s.createPrincipal)
	route("POST /v1/admin/principals/{name}/disable", s.disablePrincipal)
	route("POST /v1/admin/principals/{name}/enable", s.enablePrincipal)
	route("GET /v1/admin/tokens", s.listTokens)
	route("POST /v1/admin/tokens", s.createToken)
	route("POST /v1/admin/tokens/{id}/revoke", s.revokeToken)
	route("GET /v1/admin/audit", s.audit)
	route("PUT /v1/admin/kinds/{kind}", s.putKind)

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, domain.NotFound("no route for %s %s", r.Method, r.URL.Path))
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	n, err := db.Pending(ctx, s.svc.Store().Pool())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
		return
	}
	if n > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "migrations pending", "pending": n})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// authed authenticates the request, applies body limits and idempotency, and maps errors.
func (s *Server) authed(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			writeProblem(w, domain.Unauthenticated("missing bearer token"))
			return
		}
		c, err := s.svc.Authenticate(r.Context(), strings.TrimSpace(raw))
		if err != nil {
			if de, ok := domain.AsError(err); ok && de.Code == "unauthenticated" {
				s.svc.AuditAuthFailure(r.Context(), raw)
			}
			s.fail(w, r, err)
			return
		}
		limit := int64(maxBodyBytes)
		if r.URL.Path == "/v1/import" {
			limit = maxImportBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)

		if key := r.Header.Get("Idempotency-Key"); key != "" && r.Method == http.MethodPost {
			s.idempotent(w, r, c, key, h)
			return
		}
		if err := h(w, r, c); err != nil {
			s.fail(w, r, err)
		}
	})
}

// idempotent replays the stored response for a repeated key with the same request,
// rejects a reused key with a different request, and stores successful responses.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, c service.Caller, key string, h handler) {
	if len(key) > 200 {
		writeProblem(w, domain.BadRequest("Idempotency-Key is too long"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, r, domain.BadRequest("read body: %s", err))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.RequestURI()+"\n"), body...))

	q := s.svc.Store().Q()
	prev, err := q.GetIdempotent(r.Context(), c.Principal.ID, key)
	switch {
	case err == nil && bytes.Equal(prev.RequestHash, sum[:]):
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(prev.StatusCode)
		_, _ = w.Write(prev.Body)
		return
	case err == nil:
		writeProblem(w, domain.Unprocessable("idempotency_mismatch", "Idempotency-Key was already used with a different request"))
		return
	case !errors.Is(err, store.ErrNotFound):
		s.fail(w, r, err)
		return
	}

	buf := &bufferedWriter{header: http.Header{}, status: http.StatusOK}
	if err := h(buf, r, c); err != nil {
		s.fail(buf, r, err)
	}
	if buf.status < 500 {
		if err := q.PutIdempotent(r.Context(), c.Principal.ID, key, store.IdempotentResponse{
			RequestHash: sum[:], StatusCode: buf.status, Body: buf.body.Bytes(),
		}); err != nil {
			s.log.Error("store idempotency key", "err", err)
		}
	}
	for k, v := range buf.header {
		w.Header()[k] = v
	}
	w.WriteHeader(buf.status)
	_, _ = w.Write(buf.body.Bytes())
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		err = domain.BadRequest("request body exceeds %d bytes", mbe.Limit)
	}
	if de, ok := domain.AsError(err); ok {
		writeProblem(w, de)
		return
	}
	s.log.Error("internal error", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, &domain.Error{Status: 500, Code: "internal", Detail: "internal error, see service logs"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeProblem(w http.ResponseWriter, e *domain.Error) {
	body := map[string]any{
		"type":   "https://knowledge.local/problems/" + e.Code,
		"title":  http.StatusText(e.Status),
		"status": e.Status,
		"code":   e.Code,
		"detail": e.Detail,
	}
	for k, v := range e.Extra {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(e.Status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(body)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush lets streaming exports reach the client as they are produced.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.header }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedWriter) WriteHeader(code int)        { b.status = code }
