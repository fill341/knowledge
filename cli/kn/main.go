// Command kn is the command-line client for the Knowledge API.
//
// Configuration: KN_API_URL (default http://127.0.0.1:8787); the token comes from KN_TOKEN or
// the macOS Keychain (service "kn-token", account from --as / KN_PRINCIPAL, default "me").
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kn.local/kclient"
)

const usage = `kn — Knowledge API client

Usage: kn [--as PRINCIPAL] [--url URL] <command> [args]

Knowledge:
  search <query> [--kind K] [--project P] [--space S] [--limit N]
  get <id>
  save --kind K --title T [--body TEXT | --body-file F | --body -] [--space S | --project P]
       [--summary S] [--tag T]... [--attr key=value]... [--attributes JSON] [--source-ref R] [--draft]
  context [--project P | --repo-from-cwd] [--task T] [--max-tokens N] [--format markdown|json] [--hook]
  history <id>
  status <id> <draft|proposed|active|deprecated|superseded|archived> [--superseded-by ID] [--note N]
  promote <id> --to SPACE [--project SPACE/SLUG]
  link <from-id> <rel> <to-id>

Projects:
  projects [--space S]
  project create <space>/<slug> [--name N] [--description D] [--repo URL]... [--repo-from-cwd]
  project link <space>/<slug> [--repo URL]... [--repo-from-cwd]
                                     attach a repository (remote, or its path when it has no remote) to a project

Review:
  review                         list entries waiting for review
  review approve|reject|changes <id> [--comment C]

Data:
  export [--format jsonl|markdown] [--space S]... [--history] [-o FILE]
  import <file.jsonl> [--mode create_only|upsert_by_id] [--dry-run] [--skip-duplicate-content]

Setup:
  connect claude-code|cursor|codex [--profile personal] [--dry-run]
                                     issue the agent's token and register kn in the tool's config (MCP, hook, instructions)

Admin:
  ui [--print] [--ui-url URL]        open the web UI already signed in (one-time login link, no token pasting)
  skill export [--project P] [--format claude|cursor|agents|all] [--dir D] [--check] [--stdout]
                                     write the project's approved rules and decisions as agent files
  eval [-f eval/local/queries.json] [--baseline B] [--save-baseline] [--tolerance 0.02] [--all]
                                     retrieval quality: recall@k, MRR, context hit on a labelled query set
  health [--project P] [--limit N]   knowledge health report: duplicates, contradictions, stale, orphans…
  me
  token create <principal> <scope>... [--name N] [--expires-in-days N] [--keychain]
  token list
  token revoke <id>

Add --json to most commands for machine-readable output.
`

type app struct {
	client *kclient.Client
	as     string
	out    io.Writer
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		if ae, ok := kclient.AsError(err); ok {
			fmt.Fprintf(os.Stderr, "kn: %s\n", ae.Error())
			if ae.ExistingID != "" {
				fmt.Fprintf(os.Stderr, "    existing entry: %s\n", ae.ExistingID)
			}
		} else {
			fmt.Fprintf(os.Stderr, "kn: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("kn", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	as := global.String("as", envOr("KN_PRINCIPAL", "me"), "keychain account (principal) whose token to use")
	url := global.String("url", envOr("KN_API_URL", kclient.DefaultURL), "API base URL")
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		fmt.Print(usage)
		if err != nil && !errors.Is(err, flag.ErrHelp) {
			return err
		}
		return nil
	}
	cmd, rest := global.Arg(0), global.Args()[1:]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		fmt.Print(usage)
		return nil
	}

	a := &app{as: *as, out: os.Stdout, client: kclient.New(*url, "")}
	a.client.UserAgent = "kn-cli"
	a.client.Lang = kclient.EnvLang()
	// `context --hook` must never fail a session start, so it resolves its own token quietly.
	if cmd != "context" {
		tok, err := kclient.ResolveToken(*as)
		if err != nil {
			return err
		}
		a.client.Token = tok
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	switch cmd {
	case "me":
		return a.me(ctx, rest)
	case "ui":
		return a.ui(ctx, rest)
	case "connect":
		return a.connect(ctx, rest)
	case "health":
		return a.health(ctx, rest)
	case "eval":
		return a.eval(ctx, rest)
	case "skill":
		return a.skill(ctx, rest)
	case "search":
		return a.search(ctx, rest)
	case "get":
		return a.get(ctx, rest)
	case "save":
		return a.save(ctx, rest)
	case "context":
		return a.context(ctx, rest)
	case "history":
		return a.history(ctx, rest)
	case "status":
		return a.status(ctx, rest)
	case "promote":
		return a.promote(ctx, rest)
	case "link":
		return a.link(ctx, rest)
	case "projects":
		return a.projects(ctx, rest)
	case "project":
		return a.project(ctx, rest)
	case "review":
		return a.review(ctx, rest)
	case "export":
		return a.export(ctx, rest)
	case "import":
		return a.importFile(ctx, rest)
	case "token":
		return a.token(ctx, rest)
	default:
		return fmt.Errorf("unknown command %q (see kn help)", cmd)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// parse parses flags that may appear before, after or between positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %w", fs.Name(), err)
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		if args[0] == "--" {
			pos = append(pos, args[1:]...)
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func need(pos []string, n int, what string) error {
	if len(pos) < n {
		return fmt.Errorf("usage: kn %s", what)
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
