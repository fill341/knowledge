package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
)

// ui signs the browser in to the web UI without pasting the token: it trades the token for a
// one-time login link (valid for a minute) and opens it.
func (a *app) ui(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	base := fs.String("ui-url", envOr("KN_UI_URL", "http://127.0.0.1:8790"), "web UI base URL")
	printOnly := fs.Bool("print", false, "print the login link instead of opening the browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*base = strings.TrimRight(*base, "/")

	body, _ := json.Marshal(map[string]string{"token": a.client.Token})
	req, err := http.NewRequestWithContext(ctx, "POST", *base+"/auth/link", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KN-UI", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("web UI is not reachable at %s (is the stack up? `make up`): %w", *base, err)
	}
	defer resp.Body.Close()
	var out struct {
		Path   string `json:"path"`
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != 200 || out.Path == "" {
		return fmt.Errorf("web UI refused the login (%d): %s", resp.StatusCode, out.Detail)
	}

	link := *base + out.Path
	if *printOnly {
		fmt.Fprintln(a.out, link)
		return nil
	}
	if err := openBrowser(link); err != nil {
		fmt.Fprintf(a.out, "open this link within a minute (it works once):\n%s\n", link)
		return nil
	}
	fmt.Fprintln(a.out, "web UI opened in the browser, already signed in")
	return nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.Command("xdg-open", url).Run()
	}
}
