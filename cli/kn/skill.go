package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var errSkillStale = errors.New("skill files are out of date: run kn skill export")

// skill writes a project's approved rules and decisions as agent files (ADR-14):
// .claude/skills/kn-<slug>/SKILL.md, .cursor/rules/kn-<slug>.mdc and a marked section of AGENTS.md.
func (a *app) skill(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return fmt.Errorf("usage: kn skill export [--project P] [--format claude|cursor|agents|all] [--dir D] [--check] [--stdout]")
	}
	fs := flag.NewFlagSet("skill export", flag.ContinueOnError)
	project := fs.String("project", "", "project <space>/<slug>; default: the project of the current repository")
	format := fs.String("format", "all", "claude, cursor, agents or all")
	dir := fs.String("dir", "", "repository root to write into; default: the git root of the current directory")
	check := fs.Bool("check", false, "do not write; exit 1 when a file is missing or differs")
	stdout := fs.Bool("stdout", false, "print instead of writing")
	if _, err := parse(fs, args[1:]); err != nil {
		return err
	}
	root := *dir
	if root == "" {
		wd, _ := os.Getwd()
		out, err := exec.Command("git", "-C", wd, "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return fmt.Errorf("%s is not inside a git repository: pass --dir", wd)
		}
		root = strings.TrimSpace(string(out))
	}
	if *project == "" { // the project of the repository the files go into, not of the current directory
		p, _, err := a.client.ResolveDir(ctx, root)
		if err != nil {
			return fmt.Errorf("no project for %s: pass --project or link the repository (kn project link)", root)
		}
		*project = p.Ref
	}
	formats := []string{*format}
	if *format == "all" {
		formats = []string{"claude", "cursor", "agents"}
	}
	stale := false
	for _, f := range formats {
		sk, err := a.client.ProjectSkill(ctx, *project, f)
		if err != nil {
			return err
		}
		if *stdout {
			fmt.Fprint(a.out, sk.Content)
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(sk.Path))
		old, _ := os.ReadFile(path)
		next := sk.Content
		if f == "agents" {
			next = replaceSection(string(old), sk.Content)
		}
		switch {
		case string(old) == next:
			fmt.Fprintf(a.out, "up to date  %s\n", sk.Path)
		case *check:
			fmt.Fprintf(a.out, "stale       %s\n", sk.Path)
			stale = true
		default:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "wrote       %s (%d entries", sk.Path, len(sk.Entries))
			if sk.Indexed > 0 {
				fmt.Fprintf(a.out, ", %d by title only", sk.Indexed)
			}
			fmt.Fprintln(a.out, ")")
		}
	}
	if stale {
		return errSkillStale
	}
	return nil
}

// replaceSection swaps the kn-generated section of AGENTS.md (between the kn markers) and keeps everything else;
// without markers the section is appended.
func replaceSection(file, section string) string {
	begin := strings.Index(file, "<!-- kn:begin")
	end := strings.Index(file, "<!-- kn:end -->")
	if begin >= 0 && end > begin {
		after := file[end+len("<!-- kn:end -->"):]
		after = strings.TrimPrefix(after, "\n")
		return file[:begin] + section + after
	}
	if file == "" {
		return section
	}
	return strings.TrimRight(file, "\n") + "\n\n" + section
}
