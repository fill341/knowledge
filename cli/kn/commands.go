package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"kn.local/kclient"
)

func (a *app) me(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("me", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	me, err := a.client.Me(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(me)
	}
	fmt.Fprintf(a.out, "%s (%s)\n", me.Principal.Name, me.Principal.Kind)
	for _, s := range me.Spaces {
		fmt.Fprintf(a.out, "  %-10s %s\n", s.Slug, s.Level)
	}
	return nil
}

func (a *app) search(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	var kinds, spaces multi
	fs.Var(&kinds, "kind", "")
	fs.Var(&spaces, "space", "")
	project := fs.String("project", "", "")
	limit := fs.Int("limit", 10, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "search <query>"); err != nil {
		return err
	}
	page, err := a.client.Search(ctx, kclient.SearchRequest{Q: strings.Join(pos, " "), Kinds: kinds, Spaces: spaces,
		Project: *project, Limit: *limit})
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(page)
	}
	if len(page.Items) == 0 {
		fmt.Fprintln(a.out, "nothing found")
		return nil
	}
	for _, it := range page.Items {
		where := it.Space
		if it.Project != nil {
			where = *it.Project
		}
		status := ""
		if it.Status != "active" {
			status = " [" + it.Status + "]"
		}
		fmt.Fprintf(a.out, "%s  %s%s\n    %s · %s · score %.3f\n", it.ID, it.Title, status, it.Kind, where, it.Score)
		if h := strings.Join(strings.Fields(it.Highlight), " "); h != "" {
			fmt.Fprintf(a.out, "    %s\n", h)
		}
	}
	return nil
}

func (a *app) get(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "get <id>"); err != nil {
		return err
	}
	e, err := a.client.GetEntry(ctx, pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(e)
	}
	fmt.Fprint(a.out, renderEntry(e))
	if im, err := a.client.Impact(ctx, e.ID); err == nil && im.Count > 0 {
		fmt.Fprintf(a.out, "\nrelied on by %d (direct %d):\n", im.Count, im.Direct)
		for _, d := range im.Dependents {
			fmt.Fprintf(a.out, "  %s%s  %s (%s → %s)\n", strings.Repeat("  ", d.Depth-1), d.ID, d.Title, d.Rel, d.Via)
		}
	}
	return nil
}

func renderEntry(e kclient.EntryDetail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", e.Title)
	where := e.Space
	if e.Project != nil {
		where = *e.Project
	}
	fmt.Fprintf(&b, "%s · %s · %s · v%d · by %s · %s\n", e.ID, e.Kind, where, e.Version, e.UpdatedBy, e.UpdatedAt.Format("2006-01-02"))
	fmt.Fprintf(&b, "status: %s", e.Status)
	if e.Pinned {
		b.WriteString(" · pinned")
	}
	if len(e.Tags) > 0 {
		fmt.Fprintf(&b, " · tags: %s", strings.Join(e.Tags, ", "))
	}
	b.WriteString("\n")
	if e.SourceRef != nil {
		fmt.Fprintf(&b, "source: %s\n", *e.SourceRef)
	}
	if u := e.Usage; u != nil {
		fmt.Fprintf(&b, "usage %dd: shown %d · in context %d · opened %d\n", u.Days, u.Shown, u.Delivered, u.Opened)
	}
	if e.ReviewDue != nil {
		fmt.Fprintf(&b, "re-check by: %s\n", *e.ReviewDue)
	}
	if e.Summary != nil {
		fmt.Fprintf(&b, "\n> %s\n", *e.Summary)
	}
	if len(e.Attributes) > 0 && string(e.Attributes) != "{}" {
		var pretty map[string]any
		if json.Unmarshal(e.Attributes, &pretty) == nil {
			for k, v := range pretty {
				fmt.Fprintf(&b, "\n**%s:** %v", k, v)
			}
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(e.Body) != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(e.Body))
	}
	if len(e.Links.Outgoing)+len(e.Links.Incoming) > 0 {
		b.WriteString("\nLinks:\n")
		for _, l := range e.Links.Outgoing {
			fmt.Fprintf(&b, "  → %s %s (%s)\n", l.Rel, l.Title, l.ToID)
		}
		for _, l := range e.Links.Incoming {
			fmt.Fprintf(&b, "  ← %s %s (%s)\n", l.Rel, l.Title, l.FromID)
		}
	}
	return b.String()
}

func (a *app) save(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	kind := fs.String("kind", "note", "")
	title := fs.String("title", "", "")
	body := fs.String("body", "", "")
	bodyFile := fs.String("body-file", "", "")
	summary := fs.String("summary", "", "")
	space := fs.String("space", "", "")
	project := fs.String("project", "", "")
	attributes := fs.String("attributes", "", "")
	sourceRef := fs.String("source-ref", "", "")
	draft := fs.Bool("draft", false, "")
	pinned := fs.Bool("pinned", false, "")
	asJSON := fs.Bool("json", false, "")
	var tags, attrs multi
	fs.Var(&tags, "tag", "")
	fs.Var(&attrs, "attr", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *title == "" {
		return fmt.Errorf("--title is required")
	}
	if *space == "" && *project == "" {
		return fmt.Errorf("--space or --project is required")
	}
	text := *body
	switch {
	case *body == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(b)
	case *bodyFile != "":
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			return err
		}
		text = string(b)
	}
	var attrJSON json.RawMessage
	if *attributes != "" {
		attrJSON = json.RawMessage(*attributes)
	} else if len(attrs) > 0 {
		m := map[string]string{}
		for _, kv := range attrs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("--attr must be key=value, got %q", kv)
			}
			m[k] = v
		}
		attrJSON, _ = json.Marshal(m)
	}
	res, err := a.client.CreateEntry(ctx, kclient.CreateEntry{
		Space: *space, Project: *project, Kind: *kind, Title: *title, Body: text, Summary: strPtr(*summary),
		Tags: tags, Attributes: attrJSON, SourceRef: strPtr(*sourceRef), Draft: *draft, Pinned: *pinned,
	}, "")
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(res)
	}
	fmt.Fprintf(a.out, "saved %s (%s, v%d)\n", res.Entry.ID, res.Entry.Status, res.Entry.Version)
	printWarnings(a.out, res.Warnings)
	return nil
}

func printWarnings(w io.Writer, ws []kclient.Warning) {
	for _, wr := range ws {
		fmt.Fprintf(w, "warning: %s\n", wr.Message)
		for _, it := range wr.Items {
			if wr.Code == "duplicate_content" {
				fmt.Fprintf(w, "  %s  %s [%s]\n", it.ID, it.Title, it.Status)
			} else {
				fmt.Fprintf(w, "  %s  %s (%.2f)\n", it.ID, it.Title, it.Similarity)
			}
		}
	}
}

func (a *app) context(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	project := fs.String("project", "", "")
	fromCwd := fs.Bool("repo-from-cwd", false, "")
	task := fs.String("task", "", "")
	maxTokens := fs.Int("max-tokens", 10000, "")
	format := fs.String("format", "markdown", "")
	hook := fs.Bool("hook", false, "for session-start hooks: never fail, print nothing when empty")
	var spaces multi
	fs.Var(&spaces, "space", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	err := a.runContext(ctx, *project, *fromCwd, *task, *maxTokens, *format, spaces, *hook)
	if err != nil && *hook {
		fmt.Fprintf(os.Stderr, "kn: context unavailable: %v\n", err)
		return nil
	}
	return err
}

func (a *app) runContext(ctx context.Context, project string, fromCwd bool, task string, maxTokens int, format string, spaces []string, hook bool) error {
	tok, err := kclient.ResolveToken(a.as)
	if err != nil {
		return err
	}
	a.client.Token = tok
	req := kclient.ContextRequest{Project: project, Task: task, MaxTokens: maxTokens, Spaces: spaces}
	if fromCwd && project == "" {
		// The remote first, then the repository path (repositories without a remote); no match → space-wide context.
		wd, _ := os.Getwd()
		if p, _, err := a.client.ResolveDir(ctx, wd); err == nil {
			req.Project = p.Ref
		}
	}
	res, err := a.client.Context(ctx, req)
	if err != nil {
		return err
	}
	if format == "json" {
		return a.printJSON(res)
	}
	total := 0
	for _, s := range res.Sections {
		total += len(s.Items)
	}
	if total == 0 && hook {
		return nil
	}
	fmt.Fprint(a.out, res.Markdown)
	return nil
}

func (a *app) history(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "history <id>"); err != nil {
		return err
	}
	vs, err := a.client.Versions(ctx, pos[0])
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(vs)
	}
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "VERSION\tWHEN\tBY\tSTATUS\tNOTE")
	for _, v := range vs {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", v.Version, v.CreatedAt.Format("2006-01-02 15:04"), v.ChangedBy, v.Status, deref(v.ChangeNote))
	}
	return tw.Flush()
}

func (a *app) status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	by := fs.String("superseded-by", "", "")
	note := fs.String("note", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "status <id> <status>"); err != nil {
		return err
	}
	res, err := a.client.Transition(ctx, pos[0], pos[1], *by, *note)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s → %s (v%d)\n", res.Entry.ID, res.Entry.Status, res.Entry.Version)
	return nil
}

func (a *app) promote(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	to := fs.String("to", "", "")
	project := fs.String("project", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "promote <id> --to SPACE"); err != nil {
		return err
	}
	res, err := a.client.Promote(ctx, pos[0], *to, *project)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "promoted to %s as %s (%s)\n", res.Entry.Space, res.Entry.ID, res.Entry.Status)
	return nil
}

func (a *app) link(ctx context.Context, args []string) error {
	if err := need(args, 3, "link <from-id> <rel> <to-id>"); err != nil {
		return err
	}
	if err := a.client.AddLink(ctx, args[0], args[2], args[1]); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "linked")
	return nil
}

func (a *app) projects(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("projects", flag.ContinueOnError)
	space := fs.String("space", "", "")
	asJSON := fs.Bool("json", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	ps, err := a.client.ListProjects(ctx, *space)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(ps)
	}
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "PROJECT\tNAME\tREPOS")
	for _, p := range ps {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Ref, p.Name, strings.Join(p.RepoURLs, ", "))
	}
	return tw.Flush()
}

func (a *app) project(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "link" {
		return a.projectLink(ctx, args[1:])
	}
	if len(args) == 0 || args[0] != "create" {
		return fmt.Errorf("usage: kn project create <space>/<slug> [--name N] [--repo URL]... [--repo-from-cwd]\n" +
			"       kn project link <space>/<slug> [--repo URL] [--repo-from-cwd]")
	}
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	name := fs.String("name", "", "")
	desc := fs.String("description", "", "")
	fromCwd := fs.Bool("repo-from-cwd", false, "")
	var repos multi
	fs.Var(&repos, "repo", "")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if err := need(pos, 1, "project create <space>/<slug>"); err != nil {
		return err
	}
	space, slug, ok := strings.Cut(pos[0], "/")
	if !ok {
		return fmt.Errorf("project must be <space>/<slug>")
	}
	if *fromCwd {
		wd, _ := os.Getwd()
		ids := kclient.RepoIDs(wd)
		if len(ids) == 0 {
			return fmt.Errorf("%s is not inside a git repository", wd)
		}
		repos = append(repos, ids[0]) // the remote, or the repository path when there is none
	}
	p, err := a.client.CreateProject(ctx, space, slug, *name, *desc, repos)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "created %s (%s)\n", p.Ref, strings.Join(p.RepoURLs, ", "))
	return nil
}

func (a *app) review(ctx context.Context, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fs := flag.NewFlagSet("review", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		es, err := a.client.ReviewQueue(ctx, 100)
		if err != nil {
			return err
		}
		if *asJSON {
			return a.printJSON(es)
		}
		if len(es) == 0 {
			fmt.Fprintln(a.out, "review queue is empty")
			return nil
		}
		for _, e := range es {
			where := e.Space
			if e.Project != nil {
				where = *e.Project
			}
			fmt.Fprintf(a.out, "%s  [%s] %s\n    %s · %s · by %s\n", e.ID, e.Status, e.Title, e.Kind, where, e.CreatedBy)
			if e.Summary != nil {
				fmt.Fprintf(a.out, "    %s\n", *e.Summary)
			}
			for _, d := range e.DuplicateOf {
				fmt.Fprintf(a.out, "    ! same text as %s  %s [%s]\n", d.ID, d.Title, d.Status)
			}
		}
		fmt.Fprintln(a.out, "\nkn get <id> to read · kn review approve|reject|changes <id> [--comment C]")
		return nil
	}
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	comment := fs.String("comment", "", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 2, "review approve|reject|changes <id>"); err != nil {
		return err
	}
	action := pos[0]
	if action == "changes" {
		action = "request_changes"
	}
	for _, id := range pos[1:] {
		res, err := a.client.Review(ctx, id, action, *comment)
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		fmt.Fprintf(a.out, "%s → %s\n", res.Entry.ID, res.Entry.Status)
	}
	return nil
}

func (a *app) export(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	format := fs.String("format", "jsonl", "")
	history := fs.Bool("history", false, "")
	out := fs.String("o", "", "")
	var spaces multi
	fs.Var(&spaces, "space", "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		if *format == "markdown" {
			return fmt.Errorf("markdown export is a tar.gz archive: use -o FILE")
		}
		return a.client.Export(ctx, a.out, *format, spaces, *history)
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	err = a.client.Export(ctx, bw, *format, spaces, *history)
	// A backup that silently lost its tail is worse than a failed one: report flush and close errors too.
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "exported to %s\n", *out)
	return nil
}

func (a *app) importFile(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	mode := fs.String("mode", "create_only", "")
	dry := fs.Bool("dry-run", false, "")
	skipDup := fs.Bool("skip-duplicate-content", false, "skip new entries whose text equals a live entry of the space")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "import <file.jsonl>"); err != nil {
		return err
	}
	f, err := os.Open(pos[0])
	if err != nil {
		return err
	}
	defer f.Close()
	res, err := a.client.Import(ctx, f, kclient.ImportOptions{Mode: *mode, DryRun: *dry, SkipDuplicateContent: *skipDup})
	if err != nil {
		return err
	}
	return a.printJSON(res)
}

func (a *app) token(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kn token create|list|revoke")
	}
	switch args[0] {
	case "list":
		ts, err := a.client.ListTokens(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tPRINCIPAL\tPREFIX\tSCOPES\tLAST USED\tREVOKED")
		for _, t := range ts {
			last, rev := "-", ""
			if t.LastUsedAt != nil {
				last = t.LastUsedAt.Format("2006-01-02 15:04")
			}
			if t.RevokedAt != nil {
				rev = "revoked"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Principal, t.Prefix, strings.Join(t.Scopes, " "), last, rev)
		}
		return tw.Flush()
	case "revoke":
		if err := need(args, 2, "token revoke <id>"); err != nil {
			return err
		}
		t, err := a.client.RevokeToken(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "revoked %s (%s)\n", t.Prefix, t.Principal)
		return nil
	case "create":
		fs := flag.NewFlagSet("token create", flag.ContinueOnError)
		name := fs.String("name", "", "")
		days := fs.Int("expires-in-days", 0, "")
		keychain := fs.Bool("keychain", false, "store the token in the macOS Keychain under the principal name")
		pos, err := parse(fs, args[1:])
		if err != nil {
			return err
		}
		if err := need(pos, 2, "token create <principal> <scope>..."); err != nil {
			return err
		}
		t, err := a.client.CreateToken(ctx, kclient.CreateToken{
			Principal: pos[0], Name: *name, Scopes: pos[1:], ExpiresInDays: *days, CreatePrincipal: true,
		})
		if err != nil {
			return err
		}
		if *keychain {
			if err := kclient.StoreToken(t.Principal, t.Token); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "token for %s (%s) stored in Keychain: service %s, account %s\n",
				t.Principal, strings.Join(t.Scopes, " "), kclient.KeychainService, t.Principal)
			return nil
		}
		fmt.Fprintf(os.Stderr, "token for %s (%s) — shown once:\n", t.Principal, strings.Join(t.Scopes, " "))
		fmt.Fprintln(a.out, t.Token)
		return nil
	}
	return fmt.Errorf("usage: kn token create|list|revoke")
}

// projectLink attaches a repository to an existing project, so hooks and MCP find the project from it.
func (a *app) projectLink(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project link", flag.ContinueOnError)
	fromCwd := fs.Bool("repo-from-cwd", false, "")
	var repos multi
	fs.Var(&repos, "repo", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := need(pos, 1, "project link <space>/<slug> [--repo URL] [--repo-from-cwd]"); err != nil {
		return err
	}
	space, slug, ok := strings.Cut(pos[0], "/")
	if !ok {
		return fmt.Errorf("project must be <space>/<slug>")
	}
	if *fromCwd {
		wd, _ := os.Getwd()
		ids := kclient.RepoIDs(wd)
		if len(ids) == 0 {
			return fmt.Errorf("%s is not inside a git repository", wd)
		}
		repos = append(repos, ids[0])
	}
	if len(repos) == 0 {
		return fmt.Errorf("give --repo URL or --repo-from-cwd")
	}
	ps, err := a.client.ListProjects(ctx, space)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Slug != slug {
			continue
		}
		for _, r := range repos {
			if p, err = a.client.LinkRepo(ctx, p, r); err != nil {
				return err
			}
		}
		fmt.Fprintf(a.out, "linked %s: %s\n", p.Ref, strings.Join(p.RepoURLs, ", "))
		return nil
	}
	return fmt.Errorf("project %s not found", pos[0])
}
