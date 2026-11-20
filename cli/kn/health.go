package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"
)

var sevMark = map[string]string{"ok": "✓", "info": "·", "warn": "!", "critical": "✗"}

func (a *app) health(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	project := fs.String("project", "", "")
	limit := fs.Int("limit", 5, "")
	asJSON := fs.Bool("json", false, "")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	rep, err := a.client.Health(ctx, *project, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return a.printJSON(rep)
	}
	scope := strings.Join(rep.Spaces, ", ")
	if rep.Project != nil {
		scope = *rep.Project
	}
	fmt.Fprintf(a.out, "Здоровье базы (%s): %d/100, %s\n%s\n", scope, rep.Score, rep.Severity, rep.Summary)
	// Snapshots cover the whole base, so the trend is shown only for the unscoped report.
	if rep.Project == nil {
		if hist, err := a.client.HealthHistory(ctx, 90); err == nil && len(hist) > 1 {
			first, last := hist[0], hist[len(hist)-1]
			week, ago := first, time.Now().AddDate(0, 0, -7).Format(time.DateOnly)
			for _, h := range hist {
				if h.Day <= ago {
					week = h
				}
			}
			fmt.Fprintf(a.out, "Тренд (снимки): %s — %d, неделю назад (%s) — %d, сегодня (%s) — %d\n", first.Day, first.Score, week.Day, week.Score, last.Day, last.Score)
		}
	}
	fmt.Fprintln(a.out)
	for _, c := range rep.Checks {
		fmt.Fprintf(a.out, "[%s] %s — %s\n", sevMark[c.Severity], c.Title, c.Message)
		if c.Severity == "ok" {
			continue
		}
		for _, g := range c.Groups {
			titles := make([]string, len(g))
			for i, it := range g {
				titles[i] = fmt.Sprintf("%s (%s)", it.Title, it.ID[:8])
			}
			fmt.Fprintf(a.out, "      оставить %s; копии: %s\n", titles[0], strings.Join(titles[1:], ", "))
		}
		for _, p := range c.Pairs {
			fmt.Fprintf(a.out, "      %s (%s) ↔ %s (%s)\n", p.A.Title, p.A.ID[:8], p.B.Title, p.B.ID[:8])
		}
		for _, it := range c.Items {
			fmt.Fprintf(a.out, "      %s (%s, %s)  %s\n", it.Title, it.Kind, it.ID[:8], it.Detail)
		}
		if c.Suggestion != "" {
			fmt.Fprintf(a.out, "    → %s\n", c.Suggestion)
		}
	}
	return nil
}
