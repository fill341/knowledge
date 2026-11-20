package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"kn.local/kclient"
)

// connect wires an AI tool to the knowledge base in one step: it issues the agent's token
// (<tool>@<profile>, kept in the Keychain) and registers the MCP server, the session-start hook
// and the agent instructions in the tool's own config. Re-running it is safe: everything already
// in place is left as is, and edited JSON files get a .bak copy first.
func (a *app) connect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	profile := fs.String("profile", "personal", "space the agent writes to (personal or work); it also reads shared")
	repo := fs.String("repo", "", "path to the kn repository (default: next to the kn binary)")
	dry := fs.Bool("dry-run", false, "show what would change without changing anything")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: kn connect claude-code|cursor|codex [--profile personal] [--dry-run]")
	}
	tool := pos[0]
	if *repo == "" {
		*repo, err = repoRoot()
		if err != nil {
			return err
		}
	}
	launch := filepath.Join(*repo, "scripts", "kn-mcp-launch")
	if _, err := os.Stat(launch); err != nil {
		return fmt.Errorf("%s not found: pass --repo <path to the kn repository>", launch)
	}
	c := connector{app: a, ctx: ctx, repo: *repo, launch: launch, tool: tool, profile: *profile, dry: *dry}

	switch tool {
	case "claude-code", "cursor", "codex":
	default:
		return fmt.Errorf("unknown tool %q: claude-code, cursor or codex (ChatGPT: adapters/chatgpt/README.md)", tool)
	}
	if err := c.token(); err != nil {
		return err
	}
	switch tool {
	case "claude-code":
		err = c.claudeCode()
	case "cursor":
		err = c.cursor()
	case "codex":
		err = c.codex()
	}
	if err != nil {
		return err
	}
	if c.dry {
		fmt.Fprintln(a.out, "dry run: nothing was changed")
	} else {
		fmt.Fprintf(a.out, "done — restart %s and ask it to call kn_context\n", tool)
	}
	return nil
}

type connector struct {
	*app
	ctx                         context.Context
	repo, launch, tool, profile string
	dry                         bool
}

func (c *connector) principal() string { return c.tool + "@" + c.profile }

func (c *connector) say(format string, args ...any) {
	fmt.Fprintf(c.out, "  • "+format+"\n", args...)
}

// token reuses a working token from the Keychain or issues a new one with the admin token.
func (c *connector) token() error {
	p := c.principal()
	if tok, err := kclient.ResolveToken(p); err == nil && os.Getenv("KN_TOKEN") == "" {
		if _, err := kclient.New(c.client.BaseURL, tok).Me(c.ctx); err == nil {
			c.say("token %s: already in Keychain", p)
			return nil
		}
	}
	scopes := []string{"space:" + c.profile + ":write", "space:shared:read"}
	if c.dry {
		c.say("token %s (%s): would be issued and stored in Keychain", p, strings.Join(scopes, " "))
		return nil
	}
	t, err := c.client.CreateToken(c.ctx, kclient.CreateToken{
		Principal: p, Name: "kn connect", Scopes: scopes, CreatePrincipal: true,
	})
	if err != nil {
		return fmt.Errorf("issue token for %s (needs your admin token: make admin-token): %w", p, err)
	}
	if err := kclient.StoreToken(p, t.Token); err != nil {
		return err
	}
	c.say("token %s (%s): issued, stored in Keychain", p, strings.Join(scopes, " "))
	return nil
}

func (c *connector) claudeCode() error {
	// MCP server, user scope: available in every project.
	if _, err := exec.LookPath("claude"); err != nil {
		return errors.New("claude CLI not found in PATH: install Claude Code first")
	}
	if exec.Command("claude", "mcp", "get", "knowledge").Run() == nil {
		c.say("MCP server knowledge: already registered in Claude Code")
	} else if c.dry {
		c.say("MCP server knowledge: would run claude mcp add --scope user")
	} else {
		out, err := exec.Command("claude", "mcp", "add", "knowledge", "--scope", "user", "--",
			c.launch, c.tool, c.profile).CombinedOutput()
		if err != nil {
			return fmt.Errorf("claude mcp add: %v: %s", err, strings.TrimSpace(string(out)))
		}
		c.say("MCP server knowledge: registered (claude mcp add --scope user)")
	}

	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	// Session-start hook: project context lands in every new session. 2700 tokens because Claude Code
	// folds hook output longer than ~10 000 characters.
	hookCmd := fmt.Sprintf("KN_PRINCIPAL=%s %s context --repo-from-cwd --hook --max-tokens 2700",
		c.principal(), filepath.Join(c.repo, "bin", "kn"))
	settings := filepath.Join(dir, "settings.json")
	err := c.editJSON(settings, func(m map[string]any) bool {
		hooks, _ := m["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		start, _ := hooks["SessionStart"].([]any)
		b, _ := json.Marshal(start)
		if bytes.Contains(b, []byte(" context --repo-from-cwd --hook")) {
			return false
		}
		hooks["SessionStart"] = append(start, map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": hookCmd, "timeout": 15},
		}})
		m["hooks"] = hooks
		return true
	})
	if err != nil {
		return err
	}
	return c.appendOnce(filepath.Join(dir, "CLAUDE.md"), filepath.Join(c.repo, "adapters", "common", "AGENTS.snippet.md"))
}

func (c *connector) cursor() error {
	home, _ := os.UserHomeDir()
	err := c.editJSON(filepath.Join(home, ".cursor", "mcp.json"), func(m map[string]any) bool {
		servers, _ := m["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		if _, ok := servers["knowledge"]; ok {
			return false
		}
		servers["knowledge"] = map[string]any{"command": c.launch, "args": []string{c.tool, c.profile}}
		m["mcpServers"] = servers
		return true
	})
	if err != nil {
		return err
	}
	// User Rules live in the Cursor account, not on disk: the user pastes the rule once.
	rule := filepath.Join(c.repo, "adapters", "cursor", "knowledge.mdc")
	text, err := os.ReadFile(rule)
	if err != nil {
		return err
	}
	body := string(text)
	if i := strings.Index(body, "\n---\n"); strings.HasPrefix(body, "---\n") && i > 0 {
		body = strings.TrimLeft(body[i+5:], "\n")
	}
	_, lookErr := exec.LookPath("pbcopy")
	copied := !c.dry && lookErr == nil && pipe(body, "pbcopy") == nil
	if copied {
		c.say("rule text copied to the clipboard — paste it once into Cursor Settings → Rules → User Rules")
	} else {
		c.say("paste %s (without the --- header) into Cursor Settings → Rules → User Rules", rule)
	}
	return nil
}

func (c *connector) codex() error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".codex", "config.toml")
	cur, _ := os.ReadFile(path)
	if bytes.Contains(cur, []byte("[mcp_servers.knowledge]")) {
		c.say("%s: knowledge server already configured", path)
	} else {
		block := fmt.Sprintf("\n[mcp_servers.knowledge]\ncommand = %q\nargs = [%q, %q]\n", c.launch, c.tool, c.profile)
		if c.dry {
			c.say("%s: would add [mcp_servers.knowledge]", path)
		} else {
			if err := appendFile(path, block); err != nil {
				return err
			}
			c.say("%s: added [mcp_servers.knowledge]", path)
		}
	}
	return c.appendOnce(filepath.Join(home, ".codex", "AGENTS.md"), filepath.Join(c.repo, "adapters", "common", "AGENTS.snippet.md"))
}

// editJSON loads a JSON object (or starts an empty one), applies change and writes it back,
// keeping a .bak copy. change returns false when there is nothing to do.
func (c *connector) editJSON(path string, change func(map[string]any) bool) error {
	m := map[string]any{}
	raw, err := os.ReadFile(path)
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("%s is not valid JSON, fix it or edit by hand: %w", path, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !change(m) {
		c.say("%s: already configured", path)
		return nil
	}
	if c.dry {
		c.say("%s: would be updated", path)
		return nil
	}
	if raw != nil {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return err
		}
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return err
	}
	if raw != nil {
		c.say("%s: updated (previous version: %s.bak)", path, filepath.Base(path))
	} else {
		c.say("%s: created", path)
	}
	return nil
}

// appendOnce adds the agent instructions to a Markdown file unless they are already there.
func (c *connector) appendOnce(path, snippetPath string) error {
	snippet, err := os.ReadFile(snippetPath)
	if err != nil {
		return err
	}
	cur, _ := os.ReadFile(path)
	if bytes.Contains(cur, []byte("## База знаний (kn)")) {
		c.say("%s: kn instructions already present", path)
		return nil
	}
	if c.dry {
		c.say("%s: would append kn instructions", path)
		return nil
	}
	sep := ""
	if len(cur) > 0 {
		sep = "\n"
	}
	if err := appendFile(path, sep+string(snippet)); err != nil {
		return err
	}
	c.say("%s: kn instructions appended", path)
	return nil
}

func appendFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(text)
	return err
}

func pipe(text, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// repoRoot finds the kn repository from the binary location (<repo>/bin/kn after make build).
func repoRoot() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	root := filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(root, "scripts", "kn-mcp-launch")); err != nil {
		return "", errors.New("cannot find the kn repository next to the kn binary: run bin/kn from the repository or pass --repo")
	}
	return root, nil
}
