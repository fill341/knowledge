package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"kn.local/api/internal/domain"
)

func domainKind(k string) domain.PrincipalKind { return domain.PrincipalKind(k) }

// fixture: the human owner, one agent per profile, and a project in work.
type fixture struct {
	*env
	admin, workAgent, personalAgent string
}

func setup(t *testing.T) fixture {
	e := newEnv(t)
	f := fixture{
		env:           e,
		admin:         e.token("me", "user", "admin"),
		workAgent:     e.token("claude-code@work", "agent", "space:work:write", "space:shared:read"),
		personalAgent: e.token("claude-code@personal", "agent", "space:personal:write", "space:shared:read"),
	}
	expect(t, e.post(f.admin, "/v1/projects", map[string]any{
		"space": "work", "slug": "billing", "name": "Billing Service",
		"repo_urls": []string{"git@github.com:acme/billing.git"},
	}), 201)
	return f
}

func TestHealthAndAuth(t *testing.T) {
	f := setup(t)
	expect(t, f.get("", "/healthz"), 200)
	expect(t, f.get("", "/readyz"), 200)
	expect(t, f.get("", "/v1/me"), 401)
	expect(t, f.get("kn_AAAAAAAA_"+strings.Repeat("x", 43), "/v1/me"), 401)
	expect(t, f.get("garbage", "/v1/me"), 401)
	me := expect(t, f.get(f.workAgent, "/v1/me"), 200)
	if me.str("principal", "name") != "claude-code@work" || len(me.list("spaces")) != 2 {
		t.Fatalf("unexpected me: %s", me.Raw)
	}
}

// The core guarantee: a Work token never sees Personal knowledge, by any route.
func TestSpaceIsolation(t *testing.T) {
	f := setup(t)
	secretWord := "цеппелинокрокодил"
	p := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"space": "personal", "kind": "note", "title": "Личная заметка " + secretWord,
		"body": "Только для личного профиля: " + secretWord,
	}), 201)
	id := p.str("entry", "id")

	expect(t, f.get(f.workAgent, "/v1/entries/"+id), 404)
	expect(t, f.get(f.workAgent, "/v1/entries/"+id+"/versions"), 404)
	expect(t, f.get(f.workAgent, "/v1/entries?space=personal"), 403)
	expect(t, f.get(f.workAgent, "/v1/export?space=personal"), 403)
	expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": secretWord, "spaces": []string{"personal"}}), 403)

	list := expect(t, f.get(f.workAgent, "/v1/entries"), 200)
	if len(list.list("items")) != 0 {
		t.Fatalf("work agent listed foreign entries: %s", list.Raw)
	}
	s := expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": secretWord}), 200)
	if len(s.list("items")) != 0 {
		t.Fatalf("work agent found personal entry: %s", s.Raw)
	}
	c := expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"task": secretWord, "max_tokens": 4000}), 200)
	if strings.Contains(c.str("markdown"), secretWord) {
		t.Fatalf("personal entry leaked into work context: %s", c.str("markdown"))
	}
	exp := f.get(f.workAgent, "/v1/export")
	if strings.Contains(string(exp.Raw), secretWord) {
		t.Fatal("personal entry leaked into work export")
	}
	// Cannot write, link, transition or promote into or from personal.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "personal", "kind": "note", "title": "попытка записи", "source_ref": "test",
	}), 403)
	w := expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "рабочая заметка", "source_ref": "test",
	}), 201)
	expect(t, f.post(f.workAgent, "/v1/entries/"+w.str("entry", "id")+"/links", map[string]any{"to_id": id, "rel": "relates_to"}), 404)
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "deprecated"}), 404)
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/promote", map[string]any{"to_space": "work"}), 404)
	// Shared is read-only for agents.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "shared", "kind": "note", "title": "в общее пространство", "source_ref": "test",
	}), 403)
	// Admin endpoints are closed to agents.
	expect(t, f.get(f.workAgent, "/v1/admin/tokens"), 403)
	expect(t, f.get(f.workAgent, "/v1/review-queue"), 403)
}

func TestQualityPipeline(t *testing.T) {
	f := setup(t)
	decision := map[string]any{
		"project": "work/billing", "kind": "decision",
		"title":   "Ретраи вебхуков: экспоненциальный backoff",
		"summary": "Backoff 2^n с jitter, 8 попыток, потом DLQ.",
		"body":    "Партнёры теряли события при кратковременных падениях.",
		"attributes": map[string]any{
			"context": "Партнёры теряли события", "decision": "Экспоненциальный backoff, 8 попыток",
			"alternatives": []string{"фиксированный интервал"},
		},
		"tags": []string{"Webhooks", "reliability"},
	}

	// Agents must state provenance.
	r := expect(t, f.post(f.workAgent, "/v1/entries", decision), 422)
	if r.str("code") != "provenance_required" {
		t.Fatalf("want provenance_required: %s", r.Raw)
	}
	decision["source_ref"] = "git:acme/billing@4f2a91c"

	// Schema per kind.
	bad := clone(decision)
	bad["attributes"] = map[string]any{"context": "нет поля decision"}
	r = expect(t, f.post(f.workAgent, "/v1/entries", bad), 422)
	if r.str("code") != "validation_failed" || !strings.Contains(r.str("detail"), "decision") {
		t.Fatalf("want schema error: %s", r.Raw)
	}

	// Agent-written decisions go to review; tags are normalized.
	r = expect(t, f.post(f.workAgent, "/v1/entries", decision), 201)
	if r.str("entry", "status") != "proposed" || r.list("entry", "tags")[0] != "webhooks" {
		t.Fatalf("want proposed + normalized tags: %s", r.Raw)
	}
	if r.Header.Get("ETag") != `"1"` {
		t.Fatalf("want ETag 1, got %q", r.Header.Get("ETag"))
	}
	decisionID := r.str("entry", "id")

	// Exact duplicate.
	r = expect(t, f.post(f.workAgent, "/v1/entries", decision), 409)
	if r.str("code") != "duplicate" || r.str("existing_id") != decisionID {
		t.Fatalf("want duplicate with existing_id: %s", r.Raw)
	}

	// Similar title → warning, not an error.
	similar := clone(decision)
	similar["title"] = "Ретраи вебхуков: экспоненциальный backoff с jitter"
	similar["body"] = "другой текст"
	r = expect(t, f.post(f.workAgent, "/v1/entries", similar), 201)
	if len(r.list("warnings")) != 1 || r.list("warnings")[0].(map[string]any)["code"] != "similar_entries" {
		t.Fatalf("want similar_entries warning: %s", r.Raw)
	}

	// Secrets are rejected, and the value is not echoed back.
	leak := "ghp_" + strings.Repeat("a1B2", 9)
	r = expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Доступ к GitHub", "body": "токен: " + leak, "source_ref": "test",
	}), 422)
	if r.str("code") != "secret_detected" || strings.Contains(string(r.Raw), leak) {
		t.Fatalf("want secret_detected without the value: %s", r.Raw)
	}
	r = expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Подключение к БД", "body": "DATABASE_URL=postgres://app:S3cr3tPassw0rd@db:5432/x",
		"source_ref": "test",
	}), 422)
	// Placeholders are fine.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "snippet", "title": "Пример конфига", "body": "password=${DB_PASSWORD}\napi_key: <your-key>",
		"attributes": map[string]any{"language": "ini"}, "source_ref": "test",
	}), 201)
	// Agents cannot override the scanner; humans with review can.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Формат токенов GitHub", "body": leak, "source_ref": "test",
		"allow_secret_patterns": []string{"github-token"},
	}), 403)
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Формат токенов GitHub", "body": "Пример: " + leak,
		"allow_secret_patterns": []string{"github-token"},
	}), 201)

	// Agents cannot pin.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "rule", "title": "Всегда pnpm", "pinned": true, "source_ref": "test",
	}), 403)

	// Lessons don't need review.
	r = expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "lesson", "title": "Таймаут вебхуков в тестах",
		"attributes": map[string]any{"symptom": "flaky", "fix": "fake clock"}, "source_ref": "test",
	}), 201)
	if r.str("entry", "status") != "active" {
		t.Fatalf("lesson should be active: %s", r.Raw)
	}

	// Strict JSON catches typos.
	r = expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{"space": "work", "kind": "note", "titel": "x"}), 400)
}

func TestVersioningAndHistory(t *testing.T) {
	f := setup(t)
	r := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "fact", "title": "Вебхуки подписываются HMAC-SHA256",
		"body": "Заголовок X-Signature.",
	}), 201)
	id := r.str("entry", "id")
	path := "/v1/entries/" + id

	expect(t, f.do(f.admin, req{method: "PATCH", path: path, body: map[string]any{"body": "x"}}), 428)
	r = expect(t, f.do(f.admin, req{method: "PATCH", path: path, body: map[string]any{"body": "x"},
		headers: map[string]string{"If-Match": `"7"`}}), 412)
	if r.num("current_version") != 1 {
		t.Fatalf("want current_version 1: %s", r.Raw)
	}
	r = expect(t, f.do(f.admin, req{method: "PATCH", path: path,
		body:    map[string]any{"body": "Заголовок X-Signature, hex.", "change_note": "уточнил формат"},
		headers: map[string]string{"If-Match": `"1"`}}), 200)
	if r.num("entry", "version") != 2 {
		t.Fatalf("want version 2: %s", r.Raw)
	}
	vs := expect(t, f.get(f.admin, path+"/versions"), 200)
	if len(vs.list("items")) != 2 || vs.list("items")[0].(map[string]any)["change_note"] != "уточнил формат" {
		t.Fatalf("want 2 versions with note: %s", vs.Raw)
	}
	r = expect(t, f.do(f.admin, req{method: "POST", path: path + "/revert", body: map[string]any{"to_version": 1},
		headers: map[string]string{"If-Match": `"2"`}}), 200)
	if r.num("entry", "version") != 3 || r.str("entry", "body") != "Заголовок X-Signature." {
		t.Fatalf("revert failed: %s", r.Raw)
	}
	v1 := expect(t, f.get(f.admin, path+"/versions/1"), 200)
	if v1.str("changed_by") != "me" {
		t.Fatalf("unexpected version: %s", v1.Raw)
	}
}

func TestLifecycleAndReview(t *testing.T) {
	f := setup(t)
	r := expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "rule", "title": "Миграции только через goose", "body": "Никаких ручных ALTER.",
		"source_ref": "test",
	}), 201)
	id := r.str("entry", "id")
	if r.str("entry", "status") != "proposed" {
		t.Fatalf("want proposed: %s", r.Raw)
	}
	// Proposed entries are not in context.
	c := expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"project": "work/billing"}), 200)
	if strings.Contains(c.str("markdown"), "goose") {
		t.Fatal("proposed rule must not be in context")
	}
	// Agents cannot approve.
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "active"}), 403)
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/review", map[string]any{"action": "approve"}), 403)

	q := expect(t, f.get(f.admin, "/v1/review-queue"), 200)
	if len(q.list("items")) != 1 {
		t.Fatalf("want 1 item in review queue: %s", q.Raw)
	}
	r = expect(t, f.post(f.admin, "/v1/entries/"+id+"/review", map[string]any{"action": "approve", "comment": "ок"}), 200)
	if r.str("entry", "status") != "active" {
		t.Fatalf("want active: %s", r.Raw)
	}
	c = expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"repo_url": "https://github.com/acme/billing"}), 200)
	if !strings.Contains(c.str("markdown"), "Миграции только через goose") || c.str("project", "ref") != "work/billing" {
		t.Fatalf("approved rule must be in context resolved by repo url: %s", c.Raw)
	}

	// Agents can deprecate/supersede but not archive confirmed knowledge.
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "archived"}), 403)
	n := expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "note", "title": "Миграции через atlas", "source_ref": "test",
	}), 201)
	newID := n.str("entry", "id")
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "superseded"}), 422)
	r = expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "superseded", "superseded_by": newID}), 200)
	if r.str("entry", "superseded_by") != newID {
		t.Fatalf("superseded_by not set: %s", r.Raw)
	}
	links := expect(t, f.get(f.workAgent, "/v1/entries/"+id+"/links"), 200)
	if len(links.list("incoming")) != 1 {
		t.Fatalf("want supersedes link: %s", links.Raw)
	}
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/transition", map[string]any{"to": "draft"}), 409)
}

func TestSearchAndContext(t *testing.T) {
	f := setup(t)
	mk := func(tok string, body map[string]any) string {
		body["source_ref"] = "test"
		return expect(t, f.post(tok, "/v1/entries", body), 201).str("entry", "id")
	}
	mk(f.workAgent, map[string]any{"project": "work/billing", "kind": "lesson", "title": "Вебхуки теряются при рестарте воркера",
		"body": "Нужно подтверждать сообщение только после отправки."})
	mk(f.workAgent, map[string]any{"project": "work/billing", "kind": "lesson", "title": "Retry storm after deploy",
		"body": "Exponential backoff with jitter prevents synchronized retries."})
	mk(f.workAgent, map[string]any{"space": "work", "kind": "note", "title": "Совещание по биллингу",
		"body": "Обсудили тарифы."})
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"space": "shared", "kind": "rule",
		"title": "Коммиты по Conventional Commits", "body": "feat:, fix:, chore:", "pinned": true}), 201)

	// Russian stemming: "вебхуков" matches "Вебхуки", "теряться" matches "теряются".
	s := expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "вебхуков теряться"}), 200)
	if len(s.list("items")) == 0 || !strings.Contains(s.list("items")[0].(map[string]any)["title"].(string), "Вебхуки") {
		t.Fatalf("russian stemming failed: %s", s.Raw)
	}
	// English stemming inside the russian config: "retries" ~ "retry".
	s = expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "retries jitter"}), 200)
	if len(s.list("items")) == 0 {
		t.Fatalf("english stemming failed: %s", s.Raw)
	}
	// Fuzzy fallback on titles for typos.
	s = expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "Совещане по билингу"}), 200)
	if len(s.list("items")) == 0 {
		t.Fatalf("trigram fallback failed: %s", s.Raw)
	}
	expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "x", "mode": "hybrid"}), 422)

	// Context: pinned shared rule + project lessons + related.
	c := expect(t, f.post(f.workAgent, "/v1/context", map[string]any{
		"project": "work/billing", "task": "почему теряются вебхуки", "max_tokens": 3000,
	}), 200)
	md := c.str("markdown")
	for _, want := range []string{"Conventional Commits", "Вебхуки теряются", "Retry storm"} {
		if !strings.Contains(md, want) {
			t.Fatalf("context missing %q:\n%s", want, md)
		}
	}
	// Tiny budget → truncated, still within budget.
	c = expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"project": "work/billing", "max_tokens": 40}), 200)
	if c.get("truncated") != true || c.num("token_estimate") > 40 {
		t.Fatalf("budget not respected: %s", c.Raw)
	}
	// Markdown format for hooks.
	raw := f.do(f.workAgent, req{method: "POST", path: "/v1/context?format=markdown", body: map[string]any{"project": "work/billing"}})
	if raw.Status != 200 || !strings.HasPrefix(string(raw.Raw), "# Контекст: Billing Service") {
		t.Fatalf("markdown format: %d %s", raw.Status, raw.Raw)
	}
}

func TestIdempotency(t *testing.T) {
	f := setup(t)
	body := map[string]any{"space": "work", "kind": "note", "title": "Идемпотентная заметка", "source_ref": "test"}
	h := map[string]string{"Idempotency-Key": "abc-123"}
	a := expect(t, f.do(f.workAgent, req{method: "POST", path: "/v1/entries", body: body, headers: h}), 201)
	b := expect(t, f.do(f.workAgent, req{method: "POST", path: "/v1/entries", body: body, headers: h}), 201)
	if a.str("entry", "id") != b.str("entry", "id") || b.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay mismatch: %s vs %s", a.Raw, b.Raw)
	}
	body["title"] = "Другая заметка"
	r := expect(t, f.do(f.workAgent, req{method: "POST", path: "/v1/entries", body: body, headers: h}), 422)
	if r.str("code") != "idempotency_mismatch" {
		t.Fatalf("want idempotency_mismatch: %s", r.Raw)
	}
}

func TestSessions(t *testing.T) {
	f := setup(t)
	s := expect(t, f.post(f.workAgent, "/v1/sessions", map[string]any{
		"agent": "claude-code", "repo_url": "git@github.com:acme/billing.git", "client_info": map[string]any{"model": "x"},
	}), 201)
	if s.str("project") != "work/billing" {
		t.Fatalf("session project not resolved: %s", s.Raw)
	}
	sid := s.str("id")
	// Entries with session_id need no source_ref.
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Заметка из сессии", "session_id": sid,
	}), 201)
	// Another principal cannot use this session.
	other := f.token("cursor@work", "agent", "space:work:write")
	expect(t, f.post(other, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "Чужая сессия", "session_id": sid,
	}), 422)

	end := expect(t, f.post(f.workAgent, "/v1/sessions/"+sid+"/end", map[string]any{
		"summary": "Добавил ретраи вебхуков\n\nПодробности…",
		"outcomes": []map[string]any{
			{"kind": "lesson", "title": "Моки времени в тестах ретраев", "body": "Используй fake clock."},
			{"kind": "decision", "title": "Без поля decision"}, // fails schema, others still saved
		},
		"files_touched": []string{"webhooks/retry.go"},
	}), 200)
	outs := end.list("outcomes")
	if len(outs) != 2 || outs[0].(map[string]any)["status"] != "active" || outs[1].(map[string]any)["error"] == nil {
		t.Fatalf("unexpected outcomes: %s", end.Raw)
	}
	if !strings.HasPrefix(end.str("session_log", "title"), "Сессия claude-code: Добавил ретраи") {
		t.Fatalf("session log: %s", end.Raw)
	}
	if end.get("session", "ended_at") == nil {
		t.Fatalf("session not ended: %s", end.Raw)
	}
}

func TestPromote(t *testing.T) {
	f := setup(t)
	r := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"space": "work", "kind": "lesson", "title": "Postgres: CREATE INDEX CONCURRENTLY вне транзакции",
	}), 201)
	id := r.str("entry", "id")
	// Agents cannot promote to shared (no write there).
	expect(t, f.post(f.workAgent, "/v1/entries/"+id+"/promote", map[string]any{"to_space": "shared"}), 403)
	p := expect(t, f.post(f.admin, "/v1/entries/"+id+"/promote", map[string]any{"to_space": "shared"}), 201)
	if p.str("entry", "space") != "shared" || p.str("entry", "source_ref") != "kn:"+id {
		t.Fatalf("promote: %s", p.Raw)
	}
	// Now the personal agent can read it via shared.
	expect(t, f.get(f.personalAgent, "/v1/entries/"+p.str("entry", "id")), 200)
	expect(t, f.get(f.personalAgent, "/v1/entries/"+id), 404)
}

func TestExportImportRoundTrip(t *testing.T) {
	f := setup(t)
	a := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "decision", "title": "Храним деньги в копейках",
		"attributes": map[string]any{"context": "float", "decision": "int64 копейки"}, "tags": []string{"money"},
	}), 201).str("entry", "id")
	b := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "note", "title": "Округление по банковскому правилу",
	}), 201).str("entry", "id")
	expect(t, f.post(f.admin, "/v1/entries/"+b+"/links", map[string]any{"to_id": a, "rel": "depends_on"}), 201)
	expect(t, f.post(f.admin, "/v1/entries/"+a+"/transition", map[string]any{"to": "superseded", "superseded_by": b}), 200)

	dump := expect(t, f.get(f.admin, "/v1/export?space=work"), 200).Raw
	if n := bytes.Count(dump, []byte("\n")); n < 4 {
		t.Fatalf("export too short (%d lines): %s", n, dump)
	}

	g := setup(t) // fresh database, project already exists there
	hr, _ := http.NewRequest("POST", g.srv.URL+"/v1/import?dry_run=true", bytes.NewReader(dump))
	hr.Header.Set("Authorization", "Bearer "+g.admin)
	res, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(out), `"entries_created":2`) {
		t.Fatalf("dry run: %d %s", res.StatusCode, out)
	}
	expect(t, g.get(g.admin, "/v1/entries/"+a), 404) // dry run rolled back

	hr, _ = http.NewRequest("POST", g.srv.URL+"/v1/import", bytes.NewReader(dump))
	hr.Header.Set("Authorization", "Bearer "+g.admin)
	res, err = http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	out, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(out), `"errors":[]`) {
		t.Fatalf("import: %d %s", res.StatusCode, out)
	}
	got := expect(t, g.get(g.admin, "/v1/entries/"+a), 200)
	if got.str("status") != "superseded" || got.str("superseded_by") != b || got.str("title") != "Храним деньги в копейках" {
		t.Fatalf("imported entry differs: %s", got.Raw)
	}
	links := expect(t, g.get(g.admin, "/v1/entries/"+b+"/links"), 200)
	if len(links.list("outgoing")) != 2 { // depends_on + supersedes (created by the transition)
		t.Fatalf("links not imported: %s", links.Raw)
	}
	// Agents cannot import.
	expect(t, g.post(g.workAgent, "/v1/import", "{}"), 403)

	md := f.get(f.admin, "/v1/export?space=work&format=markdown")
	if md.Status != 200 || md.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("markdown export: %d", md.Status)
	}
}

func TestTokensAdmin(t *testing.T) {
	f := setup(t)
	r := expect(t, f.post(f.admin, "/v1/admin/tokens", map[string]any{
		"principal": "codex@work", "scopes": []string{"space:work:write"}, "create_principal": true,
	}), 201)
	tok := r.str("token")
	expect(t, f.get(tok, "/v1/me"), 200)
	expect(t, f.post(f.admin, "/v1/admin/tokens/"+r.str("id")+"/revoke", nil), 200)
	expect(t, f.get(tok, "/v1/me"), 401)
	// Agents can never hold review or admin.
	expect(t, f.post(f.admin, "/v1/admin/tokens", map[string]any{"principal": "codex@work", "scopes": []string{"space:work:review"}}), 422)
	expect(t, f.post(f.admin, "/v1/admin/tokens", map[string]any{"principal": "codex@work", "scopes": []string{"space:nope:read"}}), 422)
	audit := expect(t, f.get(f.admin, "/v1/admin/audit?action=token.revoke"), 200)
	if len(audit.list("items")) != 1 {
		t.Fatalf("audit: %s", audit.Raw)
	}
	failed := expect(t, f.get(f.admin, "/v1/admin/audit?action=auth.failed"), 200)
	if len(failed.list("items")) == 0 {
		t.Fatalf("auth failure not audited: %s", failed.Raw)
	}
	// New kinds without code changes.
	expect(t, f.do(f.admin, req{method: "PUT", path: "/v1/admin/kinds/runbook", body: map[string]any{
		"description": "Операционная инструкция",
		"attributes_schema": map[string]any{"type": "object", "required": []string{"service"},
			"properties": map[string]any{"service": map[string]any{"type": "string"}}},
	}}), 200)
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"space": "work", "kind": "runbook", "title": "Рестарт биллинга"}), 422)
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"space": "work", "kind": "runbook", "title": "Рестарт биллинга",
		"attributes": map[string]any{"service": "billing"}}), 201)
}

func TestRateLimit(t *testing.T) {
	f := setup(t)
	f.svc.WriteLimitPerMinute = 3
	for i := 0; i < 3; i++ {
		expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
			"space": "work", "kind": "note", "title": fmt.Sprintf("заметка номер %d", i), "source_ref": "t",
		}), 201)
	}
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"space": "work", "kind": "note", "title": "заметка лишняя", "source_ref": "t",
	}), 429)
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestStatsAndPrincipalDisable(t *testing.T) {
	f := setup(t)
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"space": "personal", "kind": "note", "title": "личное"}), 201)
	expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "decision", "title": "Решение агента", "source_ref": "t",
		"attributes": map[string]any{"context": "c", "decision": "d"},
	}), 201)

	st := expect(t, f.get(f.admin, "/v1/stats"), 200)
	if st.num("total") != 2 || st.num("review_queue") != 1 || st.num("projects") != 1 {
		t.Fatalf("admin stats: %s", st.Raw)
	}
	ws := expect(t, f.get(f.workAgent, "/v1/stats"), 200)
	if ws.num("total") != 1 || ws.num("review_queue") != 0 || ws.get("by_space", "personal") != nil {
		t.Fatalf("work agent stats must not count personal or review queue: %s", ws.Raw)
	}

	expect(t, f.post(f.workAgent, "/v1/admin/principals/claude-code@work/disable", nil), 403)
	expect(t, f.post(f.admin, "/v1/admin/principals/me/disable", nil), 422)
	expect(t, f.post(f.admin, "/v1/admin/principals/claude-code@work/disable", nil), 200)
	expect(t, f.get(f.workAgent, "/v1/me"), 401)
	expect(t, f.post(f.admin, "/v1/admin/principals/claude-code@work/enable", nil), 200)
	expect(t, f.get(f.workAgent, "/v1/me"), 200)
	expect(t, f.post(f.admin, "/v1/admin/principals/nobody/disable", nil), 404)
}

func TestGraphRespectsIsolation(t *testing.T) {
	f := setup(t)
	mk := func(tok string, body map[string]any) string {
		return expect(t, f.post(tok, "/v1/entries", body), 201).str("entry", "id")
	}
	personal := mk(f.admin, map[string]any{"space": "personal", "kind": "note", "title": "личный узел"})
	a := mk(f.admin, map[string]any{"project": "work/billing", "kind": "note", "title": "рабочий узел A"})
	b := mk(f.admin, map[string]any{"project": "work/billing", "kind": "note", "title": "рабочий узел B"})
	expect(t, f.post(f.admin, "/v1/entries/"+a+"/links", map[string]any{"to_id": b, "rel": "depends_on"}), 201)
	expect(t, f.post(f.admin, "/v1/entries/"+a+"/links", map[string]any{"to_id": personal, "rel": "relates_to"}), 201)
	archived := mk(f.admin, map[string]any{"space": "work", "kind": "note", "title": "архивный узел"})
	expect(t, f.post(f.admin, "/v1/entries/"+archived+"/transition", map[string]any{"to": "archived"}), 200)

	g := expect(t, f.get(f.admin, "/v1/graph"), 200)
	if len(g.list("nodes")) != 3 || len(g.list("links")) != 2 || len(g.list("projects")) != 1 {
		t.Fatalf("admin graph (archived excluded by default): %s", g.Raw)
	}
	w := expect(t, f.get(f.workAgent, "/v1/graph"), 200)
	if len(w.list("nodes")) != 2 || len(w.list("links")) != 1 || strings.Contains(string(w.Raw), "личный") {
		t.Fatalf("work agent must see only work nodes and links between them: %s", w.Raw)
	}
	// Implicit links are computed among visible entries only; explicitly linked pairs get none.
	if aff, ok := w.get("affinity").([]any); !ok || len(aff) != 0 || strings.Contains(string(w.Raw), personal) {
		t.Fatalf("work agent affinity: %s", w.Raw)
	}
	for _, n := range w.list("nodes") {
		if m, _ := n.(map[string]any)["mass"].(float64); m <= 0 {
			t.Fatalf("node without mass: %s", w.Raw)
		}
		if _, ok := n.(map[string]any)["review_due"].(bool); !ok {
			t.Fatalf("node without review_due: %s", w.Raw)
		}
		if _, ok := n.(map[string]any)["used"].(float64); !ok {
			t.Fatalf("node without used: %s", w.Raw)
		}
	}
	all := expect(t, f.get(f.admin, "/v1/graph?status=archived,active"), 200)
	if len(all.list("nodes")) != 4 {
		t.Fatalf("explicit statuses: %s", all.Raw)
	}
}

func healthCheck(t *testing.T, r resp, id string) map[string]any {
	t.Helper()
	for _, c := range r.list("checks") {
		m := c.(map[string]any)
		if m["id"] == id {
			return m
		}
	}
	t.Fatalf("check %q not in report: %s", id, r.Raw)
	return nil
}

func TestHealthReport(t *testing.T) {
	f := setup(t)
	mk := func(tok string, body map[string]any) string {
		return expect(t, f.post(tok, "/v1/entries", body), 201).str("entry", "id")
	}
	long := strings.Repeat("Ретраи вебхуков с экспоненциальной задержкой и джиттером. ", 6)

	// Identical text under different titles: one group of 3 in work; the personal copy must not join it.
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "document", "title": "docs/webhooks.md", "body": long})
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "document", "title": "import:billing/docs/webhooks.md", "body": long})
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "document", "title": "копия webhooks", "body": "  " + long + "\n"})
	mk(f.admin, map[string]any{"space": "personal", "kind": "document", "title": "личная копия", "body": long})

	// Near-duplicate titles with different text.
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "lesson", "title": "Таймауты вебхуков в интеграционных тестах", "body": "fake clock"})
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "lesson", "title": "Таймауты вебхуков в интеграционных тестах CI", "body": "увеличить таймаут"})

	// Contradiction between two live facts.
	f1 := mk(f.admin, map[string]any{"project": "work/billing", "kind": "fact", "title": "Лимит API 100 rps"})
	f2 := mk(f.admin, map[string]any{"project": "work/billing", "kind": "fact", "title": "Лимит API 500 rps"})
	expect(t, f.post(f.admin, "/v1/entries/"+f1+"/links", map[string]any{"to_id": f2, "rel": "contradicts"}), 201)

	// A decision (without alternatives — a weak entry) depends on a fact that becomes deprecated.
	dec := mk(f.admin, map[string]any{"project": "work/billing", "kind": "decision", "title": "Очередь вебхуков на Postgres",
		"attributes": map[string]any{"context": "нужна очередь", "decision": "SKIP LOCKED"}})
	old := mk(f.admin, map[string]any{"project": "work/billing", "kind": "fact", "title": "Postgres 13 в проде"})
	expect(t, f.post(f.admin, "/v1/entries/"+dec+"/links", map[string]any{"to_id": old, "rel": "depends_on"}), 201)
	expect(t, f.post(f.admin, "/v1/entries/"+old+"/transition", map[string]any{"to": "deprecated"}), 200)

	// A superseded entry whose replacement later became deprecated.
	x := mk(f.admin, map[string]any{"project": "work/billing", "kind": "note", "title": "Старый способ деплоя"})
	y := mk(f.admin, map[string]any{"project": "work/billing", "kind": "note", "title": "Новый способ деплоя"})
	expect(t, f.post(f.admin, "/v1/entries/"+x+"/transition", map[string]any{"to": "superseded", "superseded_by": y}), 200)
	expect(t, f.post(f.admin, "/v1/entries/"+y+"/transition", map[string]any{"to": "deprecated"}), 200)

	// Overdue re-check, review backlog, an empty search, a risky token.
	mk(f.admin, map[string]any{"project": "work/billing", "kind": "fact", "title": "Ключи партнёров ротируются раз в год", "review_after": "2020-01-01"})
	mk(f.workAgent, map[string]any{"project": "work/billing", "kind": "rule", "title": "Агентское правило на проверку", "source_ref": "t"})
	expect(t, f.post(f.admin, "/v1/search", map[string]any{"q": "несуществующийтерминxyz"}), 200)
	expect(t, f.post(f.admin, "/v1/admin/tokens", map[string]any{"principal": "bot@shared", "scopes": []string{"space:shared:write"}, "create_principal": true}), 201)

	r := expect(t, f.get(f.admin, "/v1/health?lang=ru"), 200)
	if r.num("score") >= 100 || r.str("severity") != "critical" {
		t.Fatalf("score/severity: %s", r.Raw)
	}
	dup := healthCheck(t, r, "duplicate_content")
	if dup["count"].(float64) != 2 || len(dup["groups"].([]any)) != 1 || len(dup["groups"].([]any)[0].([]any)) != 3 {
		t.Fatalf("duplicates: one work group of 3 (2 extra copies), personal copy excluded: %v", dup)
	}
	for id, want := range map[string]float64{"near_duplicate_titles": 1, "contradictions": 1, "depends_on_retired": 1,
		"broken_supersede": 1, "overdue_review": 1, "review_backlog": 1} {
		c := healthCheck(t, r, id)
		if c["count"].(float64) != want || c["severity"] == "ok" {
			t.Errorf("%s: want count %v and a finding, got %v", id, want, c)
		}
	}
	weak := healthCheck(t, r, "weak_entries")
	if !strings.Contains(fmt.Sprint(weak["items"]), "решение без альтернатив") {
		t.Errorf("weak entries must flag the decision without alternatives: %v", weak)
	}
	if healthCheck(t, r, "retrieval")["count"].(float64) < 1 {
		t.Errorf("empty search not counted")
	}
	acc := healthCheck(t, r, "access")
	if acc["severity"] != "warn" || !strings.Contains(fmt.Sprint(acc["tokens"]), "bot@shared") {
		t.Errorf("agent token with shared write must be flagged: %v", acc)
	}

	// Language: Accept-Language picks English texts, including the details built in SQL.
	en := expect(t, f.do(f.admin, req{method: "GET", path: "/v1/health", headers: map[string]string{"Accept-Language": "en-US,en;q=0.9"}}), 200)
	if !strings.Contains(en.str("summary"), "live entries") ||
		!strings.Contains(fmt.Sprint(healthCheck(t, en, "weak_entries")["items"]), "decision without alternatives") ||
		!strings.Contains(fmt.Sprint(healthCheck(t, en, "overdue_review")["items"]), "re-check by ") {
		t.Errorf("English report: %s", en.Raw)
	}

	// Project scope.
	pr := expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200)
	if pr.str("project") != "work/billing" || healthCheck(t, pr, "duplicate_content")["count"].(float64) != 2 {
		t.Fatalf("project report: %s", pr.Raw)
	}

	// Isolation: the work agent sees no personal data, no token audit and only its own retrieval stats.
	w := expect(t, f.get(f.workAgent, "/v1/health"), 200)
	if strings.Contains(string(w.Raw), "личная копия") || strings.Contains(string(w.Raw), "bot@shared") {
		t.Fatalf("work agent report leaks personal data or tokens: %s", w.Raw)
	}
	for _, c := range w.list("checks") {
		if c.(map[string]any)["id"] == "access" {
			t.Fatal("access check is admin-only")
		}
	}
	if healthCheck(t, w, "retrieval")["count"].(float64) != 0 {
		t.Errorf("non-admins must see only their own retrieval stats")
	}
	expect(t, f.get(f.personalAgent, "/v1/health?project=work/billing"), 403)
}

func warningIDs(r resp, code string) []string {
	var ids []string
	for _, w := range r.list("warnings") {
		m := w.(map[string]any)
		if m["code"] != code {
			continue
		}
		for _, it := range m["items"].([]any) {
			ids = append(ids, it.(map[string]any)["id"].(string))
		}
	}
	return ids
}

// Roadmap P0 #2: the same text under another title is reported on write, refused on request,
// marked in the review queue and in import results; never across spaces, never for short texts.
func TestDuplicateContentOnWrite(t *testing.T) {
	f := setup(t)
	long := strings.Repeat("Идемпотентность платежей держим ключом запроса и уникальным индексом. ", 5)
	a := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "document", "title": "docs/payments.md", "body": long}), 201).str("entry", "id")

	// Different title and formatting → saved with a warning pointing at the original.
	r := expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "document", "title": "import:billing/docs/payments.md",
		"body": "\n" + strings.ReplaceAll(long, ". ", ".\n\n") + "  "}), 201)
	if ids := warningIDs(r, "duplicate_content"); len(ids) != 1 || ids[0] != a {
		t.Fatalf("want duplicate_content warning with %s: %s", a, r.Raw)
	}
	copyID := r.str("entry", "id")

	// Reject mode for import scripts.
	r = expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "note", "title": "ещё одна копия", "body": long, "on_duplicate_content": "reject"}), 409)
	if r.str("code") != "duplicate_content" || r.str("existing_id") != a {
		t.Fatalf("reject mode: %s", r.Raw)
	}
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{
		"project": "work/billing", "kind": "note", "title": "x", "body": "y", "on_duplicate_content": "maybe"}), 422)

	// Another space and short texts are not compared.
	r = expect(t, f.post(f.admin, "/v1/entries", map[string]any{"space": "personal", "kind": "document", "title": "личная копия", "body": long}), 201)
	if len(warningIDs(r, "duplicate_content")) != 0 {
		t.Fatalf("copies across spaces are legitimate: %s", r.Raw)
	}
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"project": "work/billing", "kind": "note", "title": "Коротко 1", "body": "Да."}), 201)
	r = expect(t, f.post(f.admin, "/v1/entries", map[string]any{"project": "work/billing", "kind": "note", "title": "Коротко 2", "body": "Да."}), 201)
	if len(warningIDs(r, "duplicate_content")) != 0 {
		t.Fatalf("short texts must not be compared: %s", r.Raw)
	}

	// Editing a body into a copy warns too.
	d := expect(t, f.post(f.admin, "/v1/entries", map[string]any{"project": "work/billing", "kind": "note", "title": "Черновик про платежи", "body": "пока пусто"}), 201).str("entry", "id")
	r = expect(t, f.do(f.admin, req{method: "PATCH", path: "/v1/entries/" + d, body: map[string]any{"body": long},
		headers: map[string]string{"If-Match": `"1"`}}), 200)
	if len(warningIDs(r, "duplicate_content")) != 2 {
		t.Fatalf("update into a copy: %s", r.Raw)
	}

	// The reviewer sees that an agent's proposal copies an existing entry.
	ag := expect(t, f.post(f.workAgent, "/v1/entries", map[string]any{"project": "work/billing", "kind": "rule",
		"title": "агентская копия payments", "body": long, "source_ref": "docs/payments.md"}), 201).str("entry", "id")
	var found bool
	for _, it := range expect(t, f.get(f.admin, "/v1/review-queue"), 200).list("items") {
		m := it.(map[string]any)
		if m["id"] == ag {
			found = strings.Contains(fmt.Sprint(m["duplicate_of"]), a)
		}
	}
	if !found {
		t.Fatalf("review queue must mark the agent copy as a duplicate of %s", a)
	}

	// Import: duplicates are reported; skipped only on request. Restoring known ids is not a duplicate.
	line := func(id, title string) string {
		b, _ := json.Marshal(map[string]any{"type": "entry", "id": id, "space": "work", "project": "work/billing",
			"kind": "document", "title": title, "body": long, "status": "active", "tags": []string{}, "attributes": map[string]any{}})
		return string(b) + "\n"
	}
	dump := line(a, "docs/payments.md") + line(uuid.Must(uuid.NewV7()).String(), "repo-md:billing/docs/payments.md")
	imp := func(query string) resp {
		hr, _ := http.NewRequest("POST", f.srv.URL+"/v1/import"+query, strings.NewReader(dump))
		hr.Header.Set("Authorization", "Bearer "+f.admin)
		res, err := http.DefaultClient.Do(hr)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out resp
		out.Raw, _ = io.ReadAll(res.Body)
		_ = json.Unmarshal(out.Raw, &out.Body)
		return out
	}
	r = imp("?dry_run=true")
	if len(r.list("duplicates")) != 1 || r.num("entries_created") != 1 {
		t.Fatalf("import must report the copy and still create it by default: %s", r.Raw)
	}
	r = imp("?skip_duplicate_content=true")
	if dups := r.list("duplicates"); len(dups) != 1 || dups[0].(map[string]any)["skipped"] != true ||
		dups[0].(map[string]any)["existing_id"] != a || r.num("entries_created") != 0 {
		t.Fatalf("skip_duplicate_content: %s", r.Raw)
	}

	// The health report counts this space's copies: a + copyID + d + agent proposal.
	h := expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200)
	if c := healthCheck(t, h, "duplicate_content"); c["count"].(float64) != 3 {
		t.Fatalf("health duplicates (%s): %v", copyID, c)
	}
}

// Retrieval eval (roadmap P1 #3) on a synthetic corpus: metrics are computed through the real search and
// context code, stay above the fixture's regression floor, report label problems without leaking other
// spaces, and are not counted as searches.
func TestRetrievalEval(t *testing.T) {
	f := setup(t)
	raw, err := os.ReadFile("testdata/eval_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Min     map[string]float64 `json:"min"`
		Entries []map[string]any   `json:"entries"`
		Cases   []map[string]any   `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, e := range fx.Entries {
		key := e["key"].(string)
		delete(e, "key")
		if _, ok := e["space"]; !ok {
			e["project"] = "work/billing"
		}
		ids[key] = expect(t, f.post(f.admin, "/v1/entries", e), 201).str("entry", "id")
	}
	for _, c := range fx.Cases {
		for _, ex := range c["expect"].([]any) {
			m := ex.(map[string]any)
			if k, ok := m["key"]; ok {
				delete(m, "key")
				m["id"] = ids[k.(string)]
			}
		}
		c["project"] = "work/billing"
	}
	searches := func() float64 {
		n := 0.0
		stats, _ := healthCheck(t, expect(t, f.get(f.admin, "/v1/health"), 200), "retrieval")["stats"].([]any)
		for _, st := range stats {
			n += st.(map[string]any)["calls"].(float64)
		}
		return n
	}
	before := searches()

	r := expect(t, f.post(f.workAgent, "/v1/eval", map[string]any{"cases": fx.Cases}), 200)
	byID := map[string]map[string]any{}
	for _, c := range r.list("cases") {
		m := c.(map[string]any)
		byID[m["id"].(string)] = m
	}
	if r.num("summary", "label_errors") != 2 || byID["label-missing"]["errors"] == nil || byID["label-invisible"]["errors"] == nil {
		t.Fatalf("label errors: %s", r.Raw)
	}
	if strings.Contains(string(r.Raw), "Байкал") && !strings.Contains(fmt.Sprint(byID["label-invisible"]["query"]), "Байкал") {
		t.Fatal("eval leaks titles of another space")
	}
	for metric, floor := range fx.Min {
		if got := r.num("summary", metric); got < floor {
			t.Errorf("%s = %.3f, below the floor %.2f: %s", metric, got, floor, r.Raw)
		}
	}
	if sr := byID["synonym-miss"]["search"].(map[string]any); sr["first_rank"].(float64) != 0 || sr["rr"].(float64) != 0 {
		t.Errorf("synonym case must be a measured miss: %v", sr)
	}
	for _, id := range []string{"english-miss", "money"} {
		if sr := byID[id]["search"].(map[string]any); sr["first_rank"].(float64) < 1 {
			t.Errorf("%s: the any-word fallback must find what every-word FTS misses: %v", id, sr)
		}
	}
	if sr := byID["retry-by-source"]["search"].(map[string]any); sr["first_rank"].(float64) < 1 {
		t.Errorf("a source_ref expectation is satisfied by any of its entries: %v", sr)
	}
	if byID["money"]["context"] == nil || byID["pgbouncer"]["context"] != nil {
		t.Errorf("mode decides which retrievals run: %v / %v", byID["money"], byID["pgbouncer"])
	}
	t.Logf("fixture baseline: recall@5=%.3f recall@10=%.3f mrr=%.3f empty=%.3f context=%.3f",
		r.num("summary", "recall_at_5"), r.num("summary", "recall_at_k"), r.num("summary", "mrr"),
		r.num("summary", "empty_rate"), r.num("summary", "context_hit"))

	// Admin sees the personal entry: one label error fewer.
	if a := expect(t, f.post(f.admin, "/v1/eval", map[string]any{"cases": fx.Cases}), 200); a.num("summary", "label_errors") != 1 {
		t.Fatalf("admin label errors: %s", a.Raw)
	}
	if after := searches(); after != before {
		t.Fatalf("eval runs must not be recorded as searches: %v → %v", before, after)
	}
	expect(t, f.post(f.admin, "/v1/eval", map[string]any{"cases": []any{}}), 422)

	// Explicit operators keep their meaning: "kafka without outbox" is not broadened to "kafka or outbox".
	if items := expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "kafka -outbox"}), 200).list("items"); len(items) != 0 {
		t.Errorf("a negated query must not fall back to any-word search: %v", items)
	}
}

// Usage signal (ADR-9): search shows, context delivers, a full read opens; eval, the health report, link
// reads and denied reads do not count. The health report lists unused knowledge after 14 days of tracking.
func TestUsageSignal(t *testing.T) {
	f := setup(t)
	mk := func(body map[string]any) string {
		body["project"] = "work/billing"
		return expect(t, f.post(f.admin, "/v1/entries", body), 201).str("entry", "id")
	}
	limit := mk(map[string]any{"kind": "fact", "title": "Лимит партнёрского API — 100 rps", "body": "429 и Retry-After"})
	dec := mk(map[string]any{"kind": "decision", "title": "Очередь вебхуков на Postgres SKIP LOCKED",
		"attributes": map[string]any{"context": "нужна очередь", "decision": "SKIP LOCKED", "alternatives": []string{"Kafka"}}})
	idle := mk(map[string]any{"kind": "lesson", "title": "Урок, который никому не понадобился", "body": "никто не ищет"})

	usage := func(id string) map[string]any {
		u, _ := expect(t, f.get(f.admin, "/v1/entries/"+id), 200).get("usage").(map[string]any)
		return u
	}
	// The health report before any tracking: no verdict yet.
	if c := healthCheck(t, expect(t, f.get(f.admin, "/v1/health"), 200), "unused"); c["severity"] != "ok" || c["count"].(float64) != 0 {
		t.Fatalf("unused before tracking: %v", c)
	}

	expect(t, f.post(f.workAgent, "/v1/search", map[string]any{"q": "лимит партнёрского API"}), 200)
	expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"project": "work/billing", "task": "очередь вебхуков"}), 200)
	expect(t, f.get(f.workAgent, "/v1/entries/"+limit), 200)
	expect(t, f.get(f.workAgent, "/v1/entries/"+limit+"/links"), 200) // not a read of the entry
	expect(t, f.get(f.personalAgent, "/v1/entries/"+limit), 404)      // denied: not counted
	expect(t, f.post(f.admin, "/v1/eval", map[string]any{"cases": []any{map[string]any{
		"query": "лимит API", "project": "work/billing", "expect": []any{map[string]any{"id": limit}}}}}), 200)

	u := usage(limit) // the admin's own read is counted after the response is built
	if u["shown"].(float64) != 1 || u["opened"].(float64) != 1 || u["days"].(float64) != 30 || u["last_day"] == nil {
		t.Fatalf("limit usage (eval and denied reads must not count): %v", u)
	}
	if u := usage(dec); u["delivered"].(float64) < 1 {
		t.Fatalf("decision delivered by kn_context: %v", u)
	}
	// Pretend tracking began 20 days ago and the entries are 40 days old. The context pack above also delivered
	// the lesson (all project lessons fit the budget), so its usage is dropped to make it the idle one.
	f.ownerExec(`ALTER TABLE entries DISABLE TRIGGER entries_history`)
	f.ownerExec(`UPDATE entries SET created_at = now() - interval '40 days'`)
	f.ownerExec(`ALTER TABLE entries ENABLE TRIGGER entries_history`)
	f.ownerExec(`INSERT INTO entry_usage (entry_id, day, shown) VALUES ($1, current_date - 20, 1)
		ON CONFLICT (entry_id, day) DO NOTHING`, dec)
	f.ownerExec(`DELETE FROM entry_usage WHERE entry_id = $1`, idle)

	c := healthCheck(t, expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200), "unused")
	got := fmt.Sprint(c["items"])
	if c["severity"] != "info" || c["count"].(float64) != 1 || !strings.Contains(got, idle) || strings.Contains(got, limit) {
		t.Fatalf("unused must list only the idle lesson: %v", c)
	}
	// Isolation: the personal agent's report does not see work entries.
	if strings.Contains(string(expect(t, f.get(f.personalAgent, "/v1/health"), 200).Raw), idle) {
		t.Fatal("unused check leaks another space")
	}

	// Repeated searches without results are reported (by query hash, no text).
	for range 2 {
		expect(t, f.post(f.admin, "/v1/search", map[string]any{"q": "кракозябравонючая"}), 200)
	}
	if r := healthCheck(t, expect(t, f.get(f.admin, "/v1/health?lang=ru"), 200), "retrieval"); !strings.Contains(fmt.Sprint(r["message"]), "повторялись") {
		t.Fatalf("repeated empty searches: %v", r)
	}
}

// Context overflow: rules are capped at 35% of the budget, a task ranks what it needs first even in a crowded
// project, nothing is dropped silently (index of up to 15), the session-start context includes facts, and the
// health report warns when a project's rules no longer fit.
func TestContextOverflow(t *testing.T) {
	f := setup(t)
	post := func(body map[string]any) string {
		body["project"] = "work/billing"
		return expect(t, f.post(f.admin, "/v1/entries", body), 201).str("entry", "id")
	}
	for i := range 8 {
		post(map[string]any{"kind": "rule", "title": fmt.Sprintf("Правило оформления %d", i),
			"body": strings.Repeat(fmt.Sprintf("Подробное объяснение правила номер %d. ", i), 50)})
	}
	for i := range 40 {
		post(map[string]any{"kind": "decision", "title": fmt.Sprintf("Решение по интерфейсу экрана %d", i),
			"summary": strings.Repeat("кнопки и отступы ", 8), "attributes": map[string]any{"context": "ui", "decision": "так"}})
	}
	target := post(map[string]any{"kind": "decision", "title": "Ретраи вебхуков партнёров с джиттером",
		"summary": "Экспоненциальная задержка, 8 попыток, dead letter", "attributes": map[string]any{"context": "5xx", "decision": "backoff"}})
	fact := post(map[string]any{"kind": "fact", "title": "Лимит партнёрского API — 100 rps", "body": "429 и Retry-After"})

	ctxOf := func(task string, maxTokens int) resp {
		return expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"project": "work/billing", "task": task, "max_tokens": maxTokens}), 200)
	}
	section := func(r resp, name string) []any {
		for _, s := range r.list("sections") {
			m := s.(map[string]any)
			if m["name"] == name {
				return m["items"].([]any)
			}
		}
		return nil
	}

	r := ctxOf("ретраи вебхуков партнёров", 3000)
	md := r.str("markdown")
	if rules := md[:strings.Index(md, "## По задаче")]; float64(len([]rune(rules))) > 3000*3.5*0.36 {
		t.Fatalf("rules take more than 35%% of the budget: %d chars", len([]rune(rules)))
	}
	task := section(r, "task")
	if len(task) == 0 || task[0].(map[string]any)["id"] != target {
		t.Fatalf("the entry the task needs must come first in a crowded project: %v", task)
	}
	more := section(r, "more")
	if len(more) == 0 || len(more) > 15 || r.num("indexed_count") != float64(len(more)) || !strings.Contains(md, "kn_get") {
		t.Fatalf("overflow must be indexed (≤ 15): %d, %s", len(more), r.Raw)
	}
	if r.num("token_estimate") > 3000 {
		t.Fatalf("budget exceeded: %v", r.num("token_estimate"))
	}

	// Session start (no task): capped decisions do not crowd out facts.
	r = ctxOf("", 0)
	if !strings.Contains(fmt.Sprint(section(r, "facts")), fact) {
		t.Fatalf("session-start context must include project facts: %s", r.Raw)
	}
	if r.num("token_estimate") > 10000 || r.num("token_estimate") < 3000 {
		t.Fatalf("default budget is 10000 tokens: %v", r.num("token_estimate"))
	}

	// The rules of this project exceed 35% of the default 10000 tokens.
	if c := healthCheck(t, expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200), "rules_budget"); c["severity"] != "warn" ||
		!strings.Contains(fmt.Sprint(c["message"]), "work/billing") {
		t.Fatalf("rules budget warning: %v", c)
	}
}

// Structured session logs and handoff (ADR-11): unfinished work reaches the next agent of the project through
// kn_context until a later session lists it in "continues"; stale work shows in the health report.
func TestSessionHandoff(t *testing.T) {
	f := setup(t)
	start := func() string {
		return expect(t, f.post(f.workAgent, "/v1/sessions", map[string]any{"agent": "cursor", "project": "work/billing"}), 201).str("id")
	}
	end := func(sid string, body map[string]any, code int) resp {
		return expect(t, f.post(f.workAgent, "/v1/sessions/"+sid+"/end", body), code)
	}
	section := func(r resp, name string) string {
		for _, s := range r.list("sections") {
			if m := s.(map[string]any); m["name"] == name {
				return fmt.Sprint(m["items"])
			}
		}
		return ""
	}

	s1 := start()
	end(s1, map[string]any{"summary": "x", "status": "maybe"}, 422)
	end(s1, map[string]any{"summary": "x", "continues": []string{"not-an-id"}}, 422)
	r := end(s1, map[string]any{
		"summary": "Перевели вебхуки на очередь", "goal": "Надёжная доставка вебхуков партнёрам",
		"done": []string{"таблица webhook_jobs", "воркер с SKIP LOCKED"}, "commits": []string{"a1b2c3d"},
		"open_questions": []string{"сколько ретраев у партнёра X?"}, "next_steps": []string{"dead letter и алерт", "нагрузочный тест"},
	}, 200)
	log1 := r.str("session_log", "id")
	e := expect(t, f.get(f.admin, "/v1/entries/"+log1), 200)
	if e.str("attributes", "status") != "in_progress" || !strings.Contains(e.str("body"), "## Следующие шаги") ||
		!strings.Contains(e.str("summary"), "dead letter") {
		t.Fatalf("structured log with next steps must default to in_progress: %s", e.Raw)
	}

	// The next agent of the project gets it, with and without a task.
	for _, task := range []string{"", "алерты по вебхукам"} {
		c := expect(t, f.post(f.admin, "/v1/context", map[string]any{"project": "work/billing", "task": task}), 200)
		if !strings.Contains(section(c, "handoff"), log1) || !strings.Contains(c.str("markdown"), "Незавершённая работа") {
			t.Fatalf("handoff missing from context (task %q): %s", task, c.Raw)
		}
	}

	// A later session that continues it closes the handoff; a finished log is not a handoff.
	s2 := start()
	end(s2, map[string]any{"summary": "Сделали dead letter", "continues": []string{log1}, "done": []string{"dead letter"}}, 200)
	if c := expect(t, f.post(f.admin, "/v1/context", map[string]any{"project": "work/billing"}), 200); section(c, "handoff") != "" {
		t.Fatalf("continued work must leave the context: %s", section(c, "handoff"))
	}

	// Blocked work nobody picked up for 20 days is stale; after 30 days it leaves the context but stays in the report.
	s3 := start()
	log3 := end(s3, map[string]any{"summary": "Ждём доступ к песочнице партнёра", "status": "blocked"}, 200).str("session_log", "id")
	f.ownerExec(`ALTER TABLE entries DISABLE TRIGGER entries_history`)
	f.ownerExec(`UPDATE entries SET created_at = now() - interval '20 days' WHERE id = $1`, log3)
	f.ownerExec(`ALTER TABLE entries ENABLE TRIGGER entries_history`)
	h := healthCheck(t, expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200), "stale_handoffs")
	if h["count"].(float64) != 1 || !strings.Contains(fmt.Sprint(h["items"]), log3) {
		t.Fatalf("stale handoff: %v", h)
	}
	if c := expect(t, f.post(f.admin, "/v1/context", map[string]any{"project": "work/billing"}), 200); !strings.Contains(section(c, "handoff"), log3) {
		t.Fatal("20-day-old unfinished work is still offered")
	}
	f.ownerExec(`ALTER TABLE entries DISABLE TRIGGER entries_history`)
	f.ownerExec(`UPDATE entries SET created_at = now() - interval '40 days' WHERE id = $1`, log3)
	f.ownerExec(`ALTER TABLE entries ENABLE TRIGGER entries_history`)
	if c := expect(t, f.post(f.admin, "/v1/context", map[string]any{"project": "work/billing"}), 200); section(c, "handoff") != "" {
		t.Fatal("unfinished work older than 30 days must leave the context")
	}
}

// Freshness (ADR-12): an active entry is due for re-check at review_after, else at its last change plus its kind's
// period; due entries join the review queue and the health report and are flagged in context; approving resets it.
func TestFreshness(t *testing.T) {
	f := setup(t)
	post := func(body map[string]any) string {
		body["project"] = "work/billing"
		return expect(t, f.post(f.admin, "/v1/entries", body), 201).str("entry", "id")
	}
	fact := post(map[string]any{"kind": "fact", "title": "Партнёр X отвечает за 200 мс", "body": "p95 по замеру"})
	dec := post(map[string]any{"kind": "decision", "title": "Храним события в Kafka",
		"attributes": map[string]any{"context": "c", "decision": "d", "alternatives": []string{"a"}}})
	fresh := post(map[string]any{"kind": "fact", "title": "Свежий факт про таймауты", "body": "только что"})

	if d := expect(t, f.get(f.admin, "/v1/entries/"+fresh), 200).str("review_due"); d != time.Now().AddDate(0, 0, 180).Format(time.DateOnly) {
		t.Fatalf("a new fact is due in 180 days, got %q", d)
	}
	f.ownerExec(`ALTER TABLE entries DISABLE TRIGGER entries_history`)
	f.ownerExec(`UPDATE entries SET updated_at = now() - interval '200 days' WHERE id = ANY($1)`, []string{fact, dec})
	f.ownerExec(`ALTER TABLE entries ENABLE TRIGGER entries_history`)

	inQueue := func(id string) bool {
		for _, it := range expect(t, f.get(f.admin, "/v1/review-queue"), 200).list("items") {
			if it.(map[string]any)["id"] == id {
				return true
			}
		}
		return false
	}
	if !inQueue(fact) || inQueue(dec) || inQueue(fresh) {
		t.Fatal("only the old fact is due: decisions have no period, the fresh fact is not due yet")
	}
	if c := healthCheck(t, expect(t, f.get(f.admin, "/v1/health?project=work/billing"), 200), "overdue_review"); c["count"].(float64) != 1 ||
		!strings.Contains(fmt.Sprint(c["items"]), fact) {
		t.Fatalf("overdue_review: %v", c)
	}
	md := expect(t, f.post(f.workAgent, "/v1/context", map[string]any{"project": "work/billing", "task": "партнёр X задержка"}), 200).str("markdown")
	if !strings.Contains(md, fact+"`, пора перепроверить") || strings.Contains(md, fresh+"`, пора перепроверить") {
		t.Fatalf("context must flag only the due entry:\n%s", md)
	}

	// Approving in the review queue confirms the entry: it leaves the queue and is due again in 180 days.
	expect(t, f.post(f.admin, "/v1/entries/"+fact+"/review", map[string]any{"action": "approve"}), 200)
	if inQueue(fact) {
		t.Fatal("approved entry must leave the queue")
	}
	if d := expect(t, f.get(f.admin, "/v1/entries/"+fact), 200).str("review_due"); d != time.Now().AddDate(0, 0, 180).Format(time.DateOnly) {
		t.Fatalf("approval must restart the period, got %q", d)
	}

	// The period is a kind setting (admin); invalid values are rejected.
	expect(t, f.do(f.admin, req{method: "PUT", path: "/v1/admin/kinds/note", body: map[string]any{"description": "Заметка", "review_days": 0}}), 422)
	k := expect(t, f.do(f.admin, req{method: "PUT", path: "/v1/admin/kinds/note", body: map[string]any{"description": "Заметка", "review_days": 30}}), 200)
	if k.num("review_days") != 30 {
		t.Fatalf("kind period: %s", k.Raw)
	}
}

// Link suggestions and impact analysis (roadmap P2 #7): related structured knowledge of the same space is
// proposed on save and on request (never linked automatically); impact walks dependents up to three steps.
func TestLinkSuggestionsAndImpact(t *testing.T) {
	f := setup(t)
	post := func(tok string, body map[string]any) resp {
		if _, ok := body["space"]; !ok {
			body["project"] = "work/billing"
		}
		return expect(t, f.post(tok, "/v1/entries", body), 201)
	}
	fact := post(f.admin, map[string]any{"kind": "fact", "title": "Postgres 16 поддерживает SKIP LOCKED в очередях"}).str("entry", "id")
	dec := post(f.admin, map[string]any{"kind": "decision", "title": "Очередь вебхуков на Postgres SKIP LOCKED",
		"attributes": map[string]any{"context": "c", "decision": "d", "alternatives": []string{"Kafka"}}}).str("entry", "id")
	post(f.admin, map[string]any{"space": "personal", "kind": "decision", "title": "Личная очередь вебхуков SKIP LOCKED",
		"attributes": map[string]any{"context": "c", "decision": "d", "alternatives": []string{"x"}}})

	r := post(f.workAgent, map[string]any{"kind": "lesson", "title": "Ретраи вебхуков: SKIP LOCKED держит блокировку до коммита",
		"body": "долгая транзакция", "source_ref": "t"})
	lesson := r.str("entry", "id")
	sug := warningIDs(r, "suggested_links")
	if !slices.Contains(sug, dec) || len(sug) > 5 {
		t.Fatalf("the related decision must be suggested (same space only, at most 5): %v / %s", sug, r.Raw)
	}
	if strings.Contains(string(r.Raw), "Личная очередь") {
		t.Fatal("suggestions must not cross spaces")
	}
	if n := post(f.admin, map[string]any{"kind": "note", "title": "Заметка про SKIP LOCKED вебхуков"}); len(warningIDs(n, "suggested_links")) != 0 {
		t.Fatal("notes are not offered links")
	}

	// Linking removes the suggestion; nothing was linked automatically.
	if links := expect(t, f.get(f.admin, "/v1/entries/"+lesson+"/links"), 200); len(links.list("outgoing"))+len(links.list("incoming")) != 0 {
		t.Fatal("suggestions must never create links")
	}
	expect(t, f.post(f.workAgent, "/v1/entries/"+lesson+"/links", map[string]any{"to_id": dec, "rel": "depends_on"}), 201)
	if s := expect(t, f.get(f.workAgent, "/v1/entries/"+lesson+"/link-suggestions"), 200); strings.Contains(string(s.Raw), dec) {
		t.Fatalf("an already linked entry is not suggested again: %s", s.Raw)
	}

	// Impact of the fact: decision (1) ← lesson (2) ← note referencing the lesson (3); archived dependents are skipped.
	expect(t, f.post(f.admin, "/v1/entries/"+dec+"/links", map[string]any{"to_id": fact, "rel": "depends_on"}), 201)
	note := post(f.admin, map[string]any{"kind": "note", "title": "Разбор инцидента с блокировками"}).str("entry", "id")
	expect(t, f.post(f.admin, "/v1/entries/"+note+"/links", map[string]any{"to_id": lesson, "rel": "references"}), 201)
	im := expect(t, f.get(f.workAgent, "/v1/entries/"+fact+"/impact"), 200)
	depth := map[string]float64{}
	for _, d := range im.list("dependents") {
		m := d.(map[string]any)
		depth[m["id"].(string)] = m["depth"].(float64)
	}
	if im.num("count") != 3 || im.num("direct") != 1 || depth[dec] != 1 || depth[lesson] != 2 || depth[note] != 3 {
		t.Fatalf("impact chain: %s", im.Raw)
	}
	expect(t, f.post(f.admin, "/v1/entries/"+note+"/transition", map[string]any{"to": "archived"}), 200)
	if im := expect(t, f.get(f.admin, "/v1/entries/"+fact+"/impact"), 200); im.num("count") != 2 {
		t.Fatalf("archived dependents are not impact: %s", im.Raw)
	}
	// A retired entry also breaks the chain: with the decision archived, the lesson no longer reaches the fact.
	expect(t, f.post(f.admin, "/v1/entries/"+dec+"/transition", map[string]any{"to": "archived"}), 200)
	if im := expect(t, f.get(f.admin, "/v1/entries/"+fact+"/impact"), 200); im.num("count") != 0 {
		t.Fatalf("impact must not walk through retired entries: %s", im.Raw)
	}
	if d := expect(t, f.get(f.admin, "/v1/entries/"+dec), 200); d.get("review_due") != nil {
		t.Fatalf("retired entries have no re-check date: %s", d.Raw)
	}
	expect(t, f.get(f.personalAgent, "/v1/entries/"+fact+"/impact"), 404)
}

// Daily health snapshots (ADR-13): one per day, counts only, history for global admins only.
func TestHealthSnapshots(t *testing.T) {
	f := setup(t)
	expect(t, f.post(f.admin, "/v1/entries", map[string]any{"project": "work/billing", "kind": "fact", "title": "Факт для снимка"}), 201)
	saved, err := f.svc.SnapshotHealth(context.Background())
	if err != nil || !saved {
		t.Fatalf("first snapshot of the day: %v %v", saved, err)
	}
	if saved, err := f.svc.SnapshotHealth(context.Background()); err != nil || saved {
		t.Fatalf("a second snapshot the same day must be skipped: %v %v", saved, err)
	}
	h := expect(t, f.get(f.admin, "/v1/health/history?days=30"), 200)
	items := h.list("items")
	if len(items) != 1 {
		t.Fatalf("history: %s", h.Raw)
	}
	snap := items[0].(map[string]any)
	checks := snap["checks"].(map[string]any)
	if snap["day"] != time.Now().Format(time.DateOnly) || snap["live_entries"].(float64) != 1 || checks["orphans"] == nil || checks["access"] == nil {
		t.Fatalf("snapshot must hold the score and every check's count: %v", snap)
	}
	if strings.Contains(string(h.Raw), "Факт для снимка") {
		t.Fatal("snapshots store counts, not titles")
	}
	expect(t, f.get(f.workAgent, "/v1/health/history"), 403)
}

// Knowledge Skills (ADR-14): approved rules and decisions of a project as a native agent file; proposals stay out,
// output is deterministic, other spaces are closed.
func TestProjectSkill(t *testing.T) {
	f := setup(t)
	skill := func(tok, format string) resp {
		return f.get(tok, "/v1/projects/work/billing/skill?format="+format)
	}
	if r := expect(t, skill(f.admin, "claude"), 422); r.str("code") != "nothing_to_export" {
		t.Fatalf("empty project: %s", r.Raw)
	}
	post := func(tok string, body map[string]any) string {
		body["project"] = "work/billing"
		return expect(t, f.post(tok, "/v1/entries", body), 201).str("entry", "id")
	}
	rule := post(f.admin, map[string]any{"kind": "rule", "title": "Все POST платежей идемпотентны", "body": "Idempotency-Key обязателен"})
	dec := post(f.admin, map[string]any{"kind": "decision", "title": "Деньги в копейках int64", "summary": "никаких float",
		"attributes": map[string]any{"context": "c", "decision": "int64", "alternatives": []string{"numeric"}}})
	prop := post(f.workAgent, map[string]any{"kind": "decision", "title": "Предложение агента без проверки", "source_ref": "t",
		"attributes": map[string]any{"context": "c", "decision": "d", "alternatives": []string{"x"}}})

	r := expect(t, skill(f.workAgent, "claude"), 200)
	c := r.str("content")
	if r.str("path") != ".claude/skills/kn-billing/SKILL.md" || !strings.HasPrefix(c, "---\nname: kn-billing\ndescription: ") ||
		!strings.Contains(c, "Idempotency-Key обязателен") || !strings.Contains(c, dec) || !strings.Contains(c, rule) ||
		strings.Contains(c, prop) || strings.Contains(c, "Предложение агента") {
		t.Fatalf("claude skill (approved rules in full, decisions, no proposals): %s", r.Raw)
	}
	if again := expect(t, skill(f.workAgent, "claude"), 200).str("content"); again != c {
		t.Fatal("the skill must be deterministic")
	}
	if r := expect(t, skill(f.admin, "cursor"), 200); r.str("path") != ".cursor/rules/kn-billing.mdc" || !strings.Contains(r.str("content"), "alwaysApply: false") {
		t.Fatalf("cursor rule: %s", r.Raw)
	}
	if r := expect(t, skill(f.admin, "agents"), 200); r.str("path") != "AGENTS.md" ||
		!strings.HasPrefix(r.str("content"), "<!-- kn:begin") || !strings.HasSuffix(r.str("content"), "<!-- kn:end -->\n") {
		t.Fatalf("AGENTS.md section: %s", r.Raw)
	}
	expect(t, skill(f.admin, "pdf"), 422)
	expect(t, skill(f.personalAgent, "claude"), 403)
}
