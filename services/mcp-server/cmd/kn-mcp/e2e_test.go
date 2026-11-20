package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioEndToEnd builds kn-mcp and talks to it over stdio against a running Knowledge API.
// Set KN_E2E_API_URL and KN_E2E_TOKEN (an agent token with write access to KN_E2E_PROJECT's space).
func TestStdioEndToEnd(t *testing.T) {
	apiURL, token, project := os.Getenv("KN_E2E_API_URL"), os.Getenv("KN_E2E_TOKEN"), os.Getenv("KN_E2E_PROJECT")
	if apiURL == "" || token == "" || project == "" {
		t.Skip("KN_E2E_API_URL / KN_E2E_TOKEN / KN_E2E_PROJECT not set")
	}
	bin := filepath.Join(t.TempDir(), "kn-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "KN_API_URL="+apiURL, "KN_TOKEN="+token, "KN_AGENT=e2e-test", "KN_PROJECT="+project)

	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	callText := func(name string, args map[string]any) (string, bool) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String(), res.IsError
	}

	out, isErr := callText("kn_context", map[string]any{"task": "проверка e2e"})
	if isErr {
		t.Fatalf("kn_context: %s", out)
	}
	t.Logf("kn_context:\n%s", out)

	out, isErr = callText("kn_record_decision", map[string]any{
		"title":        "E2E: MCP-сервер как тонкий клиент REST",
		"context":      "Нужна одинаковая логика для всех агентов",
		"decision":     "kn-mcp не хранит состояние и вызывает knowledge-api",
		"alternatives": []string{"прямой доступ к БД из MCP"},
	})
	if isErr || !strings.Contains(out, "Сохранено") {
		t.Fatalf("kn_record_decision: %s", out)
	}
	t.Logf("kn_record_decision: %s", out)

	out, isErr = callText("kn_search", map[string]any{"q": "тонкий клиент"})
	if isErr || !strings.Contains(out, "E2E: MCP-сервер") {
		t.Fatalf("kn_search: %s", out)
	}

	out, isErr = callText("kn_log_session", map[string]any{
		"summary":  "E2E-проверка kn-mcp",
		"outcomes": []map[string]any{{"kind": "lesson", "title": "E2E: CommandTransport запускает сервер как дочерний процесс", "body": "stdio"}},
	})
	if isErr || !strings.Contains(out, "Итог сессии сохранён") {
		t.Fatalf("kn_log_session: %s", out)
	}
	t.Logf("kn_log_session: %s", out)

	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "kn://context/current"})
	if err != nil || len(res.Contents) == 0 {
		t.Fatalf("resource: %v", err)
	}
}
