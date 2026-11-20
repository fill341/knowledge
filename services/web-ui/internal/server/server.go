// Package server is the web UI backend-for-frontend: it serves the SPA, keeps the user's
// token in a sealed HttpOnly cookie and proxies /api/* to the Knowledge API with that token.
package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"kn.local/webui/internal/session"
)

const (
	cookieName = "kn_ui"
	sessionTTL = 12 * time.Hour
	// csrfHeader must be present on every state-changing request. Browsers cannot send a custom
	// header cross-site without a CORS preflight, which this server never approves.
	csrfHeader = "X-KN-UI"
	// linkTTL bounds how long a one-time login link from `kn ui` stays usable.
	linkTTL = time.Minute
)

type Server struct {
	api    *url.URL
	sealer *session.Sealer
	static fs.FS
	log    *slog.Logger
	proxy  *httputil.ReverseProxy
	client *http.Client
	mux    *http.ServeMux

	linksMu sync.Mutex
	links   map[string]session.Data // one-time login codes → session to open
}

func New(apiURL string, sealer *session.Sealer, static fs.FS, log *slog.Logger) (*Server, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return nil, err
	}
	s := &Server{api: u, sealer: sealer, static: static, log: log,
		client: &http.Client{Timeout: 10 * time.Second}, mux: http.NewServeMux(),
		links: map[string]session.Data{}}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(s.api)
			r.Out.URL.Path = "/v1" + strings.TrimPrefix(r.In.URL.Path, "/api")
			r.Out.URL.RawPath = ""
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del(csrfHeader)
			if d, ok := r.In.Context().Value(ctxSession{}).(session.Data); ok {
				r.Out.Header.Set("Authorization", "Bearer "+d.Token)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error("proxy", "path", r.URL.Path, "err", err)
			problem(w, http.StatusBadGateway, "api_unreachable", "Knowledge API is unreachable")
		},
	}
	s.routes()
	return s, nil
}

type ctxSession struct{}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("POST /auth/login", s.guard(s.login))
	s.mux.HandleFunc("POST /auth/link", s.guard(s.createLink))
	s.mux.HandleFunc("GET /auth/link/{code}", s.openLink)
	s.mux.HandleFunc("POST /auth/logout", s.guard(s.logout))
	s.mux.HandleFunc("GET /auth/me", s.me)
	s.mux.HandleFunc("/api/", s.guard(s.withSession(s.proxy.ServeHTTP)))
	s.mux.HandleFunc("/", s.spa)
}

// guard rejects cross-site state-changing requests.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get(csrfHeader) != "1" {
				problem(w, http.StatusForbidden, "csrf", "missing "+csrfHeader+" header")
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
				problem(w, http.StatusForbidden, "csrf", "cross-origin request rejected")
				return
			}
		}
		next(w, r)
	}
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host == host
}

func (s *Server) session(r *http.Request) (session.Data, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session.Data{}, false
	}
	d, err := s.sealer.Open(c.Value)
	return d, err == nil
}

func (s *Server) withSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.session(r)
		if !ok {
			problem(w, http.StatusUnauthorized, "unauthenticated", "sign in to the web UI")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxSession{}, d)))
	}
}

type apiMe struct {
	Principal struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"principal"`
}

func (s *Server) fetchMe(ctx context.Context, token string) (apiMe, int, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", s.api.JoinPath("/v1/me").String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return apiMe{}, 0, err
	}
	defer resp.Body.Close()
	var me apiMe
	if resp.StatusCode == 200 {
		err = json.NewDecoder(resp.Body).Decode(&me)
	}
	return me, resp.StatusCode, err
}

// readToken decodes {"token": "..."} and checks that it belongs to a human principal.
// On failure it has already written the problem response.
func (s *Server) readToken(w http.ResponseWriter, r *http.Request) (session.Data, bool) {
	var in struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || strings.TrimSpace(in.Token) == "" {
		problem(w, http.StatusBadRequest, "bad_request", "token is required")
		return session.Data{}, false
	}
	token := strings.TrimSpace(in.Token)
	me, status, err := s.fetchMe(r.Context(), token)
	switch {
	case err != nil:
		s.log.Error("login: api", "err", err)
		problem(w, http.StatusBadGateway, "api_unreachable", "Knowledge API is unreachable")
		return session.Data{}, false
	case status != 200:
		problem(w, http.StatusUnauthorized, "unauthenticated", "token is invalid, expired or revoked")
		return session.Data{}, false
	case me.Principal.Kind == "agent":
		problem(w, http.StatusForbidden, "forbidden", "agent tokens cannot sign in to the web UI; use a human principal's token")
		return session.Data{}, false
	}
	return session.Data{Token: token, Principal: me.Principal.Name}, true
}

func (s *Server) setSession(w http.ResponseWriter, d session.Data) error {
	d.Expires = time.Now().Add(sessionTTL)
	val, err := s.sealer.Seal(d)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: val, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
	return nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	d, ok := s.readToken(w, r)
	if !ok {
		return
	}
	if err := s.setSession(w, d); err != nil {
		problem(w, http.StatusInternalServerError, "internal", "cannot create session")
		return
	}
	writeJSON(w, 200, map[string]string{"principal": d.Principal})
}

// createLink trades a human token for a one-time login link (`kn ui`), so the browser signs in
// without pasting the token and the token never appears in a URL or browser history.
func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	d, ok := s.readToken(w, r)
	if !ok {
		return
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		problem(w, http.StatusInternalServerError, "internal", "cannot create link")
		return
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	d.Expires = time.Now().Add(linkTTL)
	s.linksMu.Lock()
	for c, v := range s.links {
		if time.Now().After(v.Expires) {
			delete(s.links, c)
		}
	}
	s.links[code] = d
	s.linksMu.Unlock()
	writeJSON(w, 200, map[string]any{"path": "/auth/link/" + code, "expires": d.Expires})
}

func (s *Server) openLink(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	s.linksMu.Lock()
	d, ok := s.links[code]
	delete(s.links, code)
	s.linksMu.Unlock()
	if !ok || time.Now().After(d.Expires) {
		http.Redirect(w, r, "/?login=expired", http.StatusSeeOther)
		return
	}
	if err := s.setSession(w, d); err != nil {
		problem(w, http.StatusInternalServerError, "internal", "cannot create session")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// me reports whether the browser has a session and whether its token still works.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	d, ok := s.session(r)
	if !ok {
		problem(w, http.StatusUnauthorized, "unauthenticated", "not signed in")
		return
	}
	_, status, err := s.fetchMe(r.Context(), d.Token)
	if err != nil {
		problem(w, http.StatusBadGateway, "api_unreachable", "Knowledge API is unreachable")
		return
	}
	if status != 200 {
		problem(w, http.StatusUnauthorized, "unauthenticated", "token was revoked or expired; sign in again")
		return
	}
	writeJSON(w, 200, map[string]any{"principal": d.Principal, "expires": d.Expires})
}

// spa serves built assets and falls back to index.html for client-side routes.
func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p != "" {
		if st, err := fs.Stat(s.static, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.static, p)
			return
		}
	}
	index, err := fs.ReadFile(s.static, "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "frontend is not built: run `make ui-build` (or build the Docker image)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail})
}
