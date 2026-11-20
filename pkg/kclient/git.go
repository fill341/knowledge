package kclient

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitRemote returns the URL of the "origin" remote of the repository in dir, or "".
func GitRemote(dir string) string {
	return git(dir, "remote", "get-url", "origin")
}

// RepoIDs identifies the git repository containing dir, best first: the origin remote (stable across machines),
// then file://<repository root> for repositories without a remote. Empty outside a git repository, so a plain
// folder (for example $HOME, where some clients start MCP servers) never matches a project.
func RepoIDs(dir string) []string {
	root := git(dir, "rev-parse", "--show-toplevel")
	if root == "" {
		return nil
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	var ids []string
	if r := GitRemote(dir); r != "" {
		ids = append(ids, r)
	}
	return append(ids, (&url.URL{Scheme: "file", Path: root}).String())
}

func git(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
