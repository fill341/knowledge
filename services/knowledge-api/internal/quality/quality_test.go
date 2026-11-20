package quality

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScanSecrets(t *testing.T) {
	positive := map[string]string{
		"private-key":     "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbn...",
		"aws-access-key":  "key AKIAIOSFODNN7EXAMPLE here",
		"github-token":    "ghp_" + strings.Repeat("aB3d", 9),
		"gitlab-token":    "glpat-" + strings.Repeat("x9Y", 8),
		"slack-token":     "xoxb-123456789012-abcdefghij",
		"api-key-sk":      "sk-ant-" + strings.Repeat("Qw9", 10),
		"jwt":             "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"kn-token":        "kn_AbCd1234_" + strings.Repeat("Zx81", 11),
		"url-credentials": "postgres://app:S3cr3tPassw0rd@db:5432/x",
		"assignment":      `DB_PASSWORD="Kq8#vL2!pZ9w"`,
	}
	for rule, text := range positive {
		f := ScanSecrets(map[string]string{"body": text}, nil)
		if len(f) == 0 || f[0].Rule != rule {
			t.Errorf("%s: expected detection in %q, got %+v", rule, text, f)
		}
	}
	negative := []string{
		"password=${DB_PASSWORD}",
		"api_key: <your-key>",
		"token: changeme",
		"password: xxxxxxxxxxxx",
		"postgres://user:${PASS}@localhost/db",
		"Используем токены доступа с ротацией раз в 90 дней",
		"set the token in KN_TOKEN env var",
		"sk-learn is a python library",
	}
	for _, text := range negative {
		if f := ScanSecrets(map[string]string{"body": text}, nil); len(f) > 0 {
			t.Errorf("false positive for %q: %+v", text, f)
		}
	}
	if f := ScanSecrets(map[string]string{"body": positive["github-token"]}, []string{"github-token"}); len(f) != 0 {
		t.Errorf("allow list ignored: %+v", f)
	}
}

func TestSecretFieldsIncludesAttributes(t *testing.T) {
	f := SecretFields("t", nil, "b", json.RawMessage(`{"a": {"b": ["x", "ghp_secret"]}}`))
	if f["attributes.a.b[1]"] != "ghp_secret" {
		t.Fatalf("nested attribute not collected: %v", f)
	}
}

func TestNormalizeTags(t *testing.T) {
	got, err := NormalizeTags([]string{" Webhooks ", "webhooks", "", "go-lang"})
	if err != nil || strings.Join(got, ",") != "webhooks,go-lang" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := NormalizeTags([]string{"with space"}); err == nil {
		t.Fatal("expected error for invalid tag")
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if _, err := NormalizeTags(many); err == nil {
		t.Fatal("expected error for too many tags")
	}
}

func TestContentHash(t *testing.T) {
	a := ContentHash("note", "  Заголовок ", "текст   с\n\nпробелами")
	b := ContentHash("note", "заголовок", "текст с пробелами")
	if string(a) != string(b) {
		t.Fatal("hash should ignore case of title and whitespace in body")
	}
	if string(a) == string(ContentHash("fact", "заголовок", "текст с пробелами")) {
		t.Fatal("hash should depend on kind")
	}
}

func TestSchemaValidation(t *testing.T) {
	s := NewSchemas()
	schema := json.RawMessage(`{"type":"object","required":["decision"],"additionalProperties":false,
		"properties":{"decision":{"type":"string","minLength":1},"decided_at":{"type":"string","format":"date"}}}`)
	if err := s.Validate("decision", schema, json.RawMessage(`{"decision":"x","decided_at":"2026-01-02"}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"decision":""}`, `{"decision":"x","extra":1}`, `{"decision":"x","decided_at":"вчера"}`} {
		if err := s.Validate("decision", schema, json.RawMessage(bad)); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}
