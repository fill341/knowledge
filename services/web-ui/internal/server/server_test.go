package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"kn.local/webui/internal/session"
)

// fakeAPI accepts "kn_user" (human) and "kn_agent" (agent) tokens and echoes what it received.
func fakeAPI(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		kind := map[string]string{"Bearer kn_user": "user", "Bearer kn_agent": "agent"}[auth]
		if kind == "" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/me" {
			_ = json.NewEncoder(w).Encode(map[string]any{"principal": map[string]string{"name": kind + "-name", "kind": kind}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"path": r.URL.Path, "query": r.URL.RawQuery, "auth": auth, "cookie": r.Header.Get("Cookie"),
		})
	}))
}

func newUI(t *testing.T) (*httptest.Server, *http.Client) {
	api := fakeAPI(t)
	t.Cleanup(api.Close)
	sealer, _ := session.NewSealer([]byte("test-key-test-key-test-key"))
	assets := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>kn</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv, err := New(api.URL, sealer, assets, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ui := httptest.NewServer(srv)
	t.Cleanup(ui.Close)
	jar, _ := cookiejar.New(nil)
	return ui, &http.Client{Jar: jar}
}

func do(t *testing.T, c *http.Client, method, url, body string, csrf bool) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if csrf {
		req.Header.Set("X-KN-UI", "1")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestLoginAndProxy(t *testing.T) {
	ui, c := newUI(t)

	if r, _ := do(t, c, "GET", ui.URL+"/api/me", "", false); r.StatusCode != 401 {
		t.Fatalf("api without session: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/auth/login", `{"token":"kn_user"}`, false); r.StatusCode != 403 {
		t.Fatalf("login without CSRF header must fail: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/auth/login", `{"token":"kn_agent"}`, true); r.StatusCode != 403 {
		t.Fatalf("agent tokens must not sign in: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/auth/login", `{"token":"bad"}`, true); r.StatusCode != 401 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	r, body := do(t, c, "POST", ui.URL+"/auth/login", `{"token":"kn_user"}`, true)
	if r.StatusCode != 200 || !strings.Contains(body, "user-name") {
		t.Fatalf("login: %d %s", r.StatusCode, body)
	}
	cookie := r.Header.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Strict") || strings.Contains(cookie, "kn_user") {
		t.Fatalf("cookie must be HttpOnly, SameSite=Strict and not contain the raw token: %s", cookie)
	}

	r, body = do(t, c, "GET", ui.URL+"/api/entries?status=proposed", "", false)
	var echo map[string]string
	_ = json.Unmarshal([]byte(body), &echo)
	if r.StatusCode != 200 || echo["path"] != "/v1/entries" || echo["query"] != "status=proposed" ||
		echo["auth"] != "Bearer kn_user" || echo["cookie"] != "" {
		t.Fatalf("proxy must map /api→/v1, inject the token and drop cookies: %d %s", r.StatusCode, body)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/api/entries", `{}`, false); r.StatusCode != 403 {
		t.Fatalf("state-changing proxy call without CSRF header: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", ui.URL+"/api/entries", strings.NewReader(`{}`))
	req.Header.Set("X-KN-UI", "1")
	req.Header.Set("Origin", "http://evil.example")
	if resp, _ := c.Do(req); resp.StatusCode != 403 {
		t.Fatalf("cross-origin POST must be rejected: %d", resp.StatusCode)
	}
	if r, _ := do(t, c, "GET", ui.URL+"/auth/me", "", false); r.StatusCode != 200 {
		t.Fatalf("me: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/auth/logout", "", true); r.StatusCode != 204 {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "GET", ui.URL+"/api/entries", "", false); r.StatusCode != 401 {
		t.Fatalf("after logout: %d", r.StatusCode)
	}
}

func TestLoginLink(t *testing.T) {
	ui, c := newUI(t)

	if r, _ := do(t, c, "POST", ui.URL+"/auth/link", `{"token":"kn_user"}`, false); r.StatusCode != 403 {
		t.Fatalf("link without CSRF header must fail: %d", r.StatusCode)
	}
	if r, _ := do(t, c, "POST", ui.URL+"/auth/link", `{"token":"kn_agent"}`, true); r.StatusCode != 403 {
		t.Fatalf("agent tokens must not get a link: %d", r.StatusCode)
	}
	r, body := do(t, c, "POST", ui.URL+"/auth/link", `{"token":"kn_user"}`, true)
	var link struct{ Path string }
	_ = json.Unmarshal([]byte(body), &link)
	if r.StatusCode != 200 || !strings.HasPrefix(link.Path, "/auth/link/") || strings.Contains(body, "kn_user") {
		t.Fatalf("create link: %d %s", r.StatusCode, body)
	}
	if r.Header.Get("Set-Cookie") != "" {
		t.Fatal("creating a link must not sign in the caller")
	}

	browser := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	b := browser()
	r, _ = do(t, b, "GET", ui.URL+link.Path, "", false)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/" || !strings.Contains(r.Header.Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("open link: %d %s %s", r.StatusCode, r.Header.Get("Location"), r.Header.Get("Set-Cookie"))
	}
	if r, _ := do(t, b, "GET", ui.URL+"/auth/me", "", false); r.StatusCode != 200 {
		t.Fatalf("session after link: %d", r.StatusCode)
	}

	b2 := browser()
	r, _ = do(t, b2, "GET", ui.URL+link.Path, "", false)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/?login=expired" || r.Header.Get("Set-Cookie") != "" {
		t.Fatalf("a link must work once: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestTamperedCookieRejected(t *testing.T) {
	ui, _ := newUI(t)
	req, _ := http.NewRequest("GET", ui.URL+"/api/entries", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("tampered cookie: %d", resp.StatusCode)
	}
}

func TestSPA(t *testing.T) {
	ui, c := newUI(t)
	r, body := do(t, c, "GET", ui.URL+"/review/some-id", "", false)
	if r.StatusCode != 200 || !strings.Contains(body, "<title>kn</title>") {
		t.Fatalf("client routes must fall back to index.html: %d %s", r.StatusCode, body)
	}
	r, _ = do(t, c, "GET", ui.URL+"/assets/app.js", "", false)
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("assets: %d %s", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("CSP header missing")
	}
}

func TestSealer(t *testing.T) {
	s, _ := session.NewSealer([]byte("0123456789abcdef0123"))
	other, _ := session.NewSealer([]byte("another-key-another-key"))
	v, _ := s.Seal(session.Data{Token: "t", Principal: "me", Expires: farFuture()})
	if d, err := s.Open(v); err != nil || d.Token != "t" {
		t.Fatalf("open: %v %+v", err, d)
	}
	if _, err := other.Open(v); err == nil {
		t.Fatal("a different key must not open the session")
	}
	expired, _ := s.Seal(session.Data{Token: "t", Expires: farFuture().AddDate(-100, 0, 0)})
	if _, err := s.Open(expired); err == nil {
		t.Fatal("expired session accepted")
	}
}

func farFuture() time.Time { return time.Now().Add(time.Hour) }
