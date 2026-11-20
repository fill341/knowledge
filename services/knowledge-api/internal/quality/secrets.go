// Package quality implements write-time checks: secret scanning, attribute schemas,
// structural validation and content hashing for deduplication.
package quality

import (
	"math"
	"regexp"
	"strings"
)

type secretRule struct {
	id string
	re *regexp.Regexp
	// group is the capture group holding the secret value, for entropy checks (0 = whole match).
	group int
	// minEntropy filters out placeholders like password=changeme (bits per char).
	minEntropy float64
}

var secretRules = []secretRule{
	{id: "private-key", re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY( BLOCK)?-----`)},
	{id: "aws-access-key", re: regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{id: "github-token", re: regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{22,}\b`)},
	{id: "gitlab-token", re: regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{id: "slack-token", re: regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`)},
	{id: "api-key-sk", re: regexp.MustCompile(`\bsk-(ant-|proj-|live-|test-)?[A-Za-z0-9_-]{20,}\b`)},
	{id: "google-api-key", re: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{id: "jwt", re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)},
	{id: "kn-token", re: regexp.MustCompile(`\bkn_[0-9A-Za-z]{8}_[0-9A-Za-z]{32,}\b`)},
	{id: "url-credentials", re: regexp.MustCompile(`\b[a-z][a-z0-9+.-]*://[^\s:/@]+:([^\s@/]{3,})@`), group: 1, minEntropy: 2.5},
	// Names may be part of a longer identifier (DB_PASSWORD, secret_key), so no \b on the left.
	{id: "assignment", re: regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key)[a-z0-9_]*["']?\s*[:=]\s*["']?([^\s"',;]{8,})`), group: 2, minEntropy: 3.0},
}

// Finding locates a detected secret without carrying its value.
type Finding struct {
	Rule   string `json:"rule"`
	Field  string `json:"field"`
	Offset int    `json:"offset"`
}

// ScanSecrets checks each named field. allow lists rule IDs to skip (human override).
func ScanSecrets(fields map[string]string, allow []string) []Finding {
	skip := map[string]bool{}
	for _, a := range allow {
		skip[a] = true
	}
	var out []Finding
	for field, text := range fields {
		if text == "" {
			continue
		}
		for _, r := range secretRules {
			if skip[r.id] {
				continue
			}
			for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
				if r.minEntropy > 0 {
					v := text[m[2*r.group]:m[2*r.group+1]]
					if looksLikePlaceholder(v) || entropy(v) < r.minEntropy {
						continue
					}
				}
				out = append(out, Finding{Rule: r.id, Field: field, Offset: m[0]})
				break
			}
		}
	}
	return out
}

func looksLikePlaceholder(v string) bool {
	l := strings.ToLower(v)
	for _, p := range []string{"xxx", "***", "<", "${", "{{", "example", "changeme", "your_", "your-", "redacted", "placeholder", "env("} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// entropy is the Shannon entropy in bits per character.
func entropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := map[rune]float64{}
	n := 0.0
	for _, r := range s {
		freq[r]++
		n++
	}
	var h float64
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
