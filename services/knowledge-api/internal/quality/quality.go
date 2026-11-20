package quality

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

var (
	tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	space = regexp.MustCompile(`\s+`)
)

const MaxTags = 10

// NormalizeTags lowercases, trims, dedupes and validates tags.
func NormalizeTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if !tagRe.MatchString(t) {
			return nil, fmt.Errorf("invalid tag %q: use lowercase letters, digits and dashes, up to 32 chars", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > MaxTags {
		return nil, fmt.Errorf("too many tags: %d (max %d)", len(out), MaxTags)
	}
	return out, nil
}

// CheckLengths mirrors the database CHECK constraints with friendlier errors.
func CheckLengths(title string, summary *string, body string) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(title)); n < 3 || n > 300 {
		return fmt.Errorf("title must be 3..300 characters, got %d", n)
	}
	if summary != nil && utf8.RuneCountInString(*summary) > 1000 {
		return fmt.Errorf("summary must be at most 1000 characters")
	}
	if utf8.RuneCountInString(body) > 200000 {
		return fmt.Errorf("body must be at most 200000 characters")
	}
	return nil
}

// ContentHash identifies exact duplicates: kind, case-insensitive title and whitespace-collapsed body.
func ContentHash(kind, title, body string) []byte {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(strings.TrimSpace(title))))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(space.ReplaceAllString(body, " "))))
	return h.Sum(nil)
}

// SecretFields collects the text fields that must be scanned, including string values in attributes.
func SecretFields(title string, summary *string, body string, attributes json.RawMessage) map[string]string {
	f := map[string]string{"title": title, "body": body}
	if summary != nil {
		f["summary"] = *summary
	}
	var v any
	if len(attributes) > 0 && json.Unmarshal(attributes, &v) == nil {
		collectStrings("attributes", v, f)
	}
	return f
}

func collectStrings(path string, v any, out map[string]string) {
	switch t := v.(type) {
	case string:
		out[path] = t
	case map[string]any:
		for k, vv := range t {
			collectStrings(path+"."+k, vv, out)
		}
	case []any:
		for i, vv := range t {
			collectStrings(fmt.Sprintf("%s[%d]", path, i), vv, out)
		}
	}
}

// Schemas compiles and caches JSON Schemas for entry kinds.
type Schemas struct {
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	raw    string
	schema *jsonschema.Schema
}

func NewSchemas() *Schemas { return &Schemas{cache: map[string]cached{}} }

// Validate checks attributes against the kind's schema. The compiled schema is cached
// and recompiled when the stored schema text changes.
func (s *Schemas) Validate(kind string, schema json.RawMessage, attributes json.RawMessage) error {
	if len(schema) == 0 || bytes.Equal(bytes.TrimSpace(schema), []byte("{}")) {
		return nil
	}
	sch, err := s.compiled(kind, schema)
	if err != nil {
		return fmt.Errorf("schema for kind %q is invalid: %w", kind, err)
	}
	if len(attributes) == 0 {
		attributes = json.RawMessage(`{}`)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(attributes))
	if err != nil {
		return fmt.Errorf("attributes: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("attributes do not match schema for kind %q: %s", kind, flatten(err))
	}
	return nil
}

func (s *Schemas) compiled(kind string, schema json.RawMessage) (*jsonschema.Schema, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[kind]; ok && c.raw == string(schema) {
		return c.schema, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	url := "kind://" + kind
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, err
	}
	s.cache[kind] = cached{raw: string(schema), schema: sch}
	return sch, nil
}

// CompileCheck validates that a schema document compiles (used when admins edit kinds).
func CompileCheck(schema json.RawMessage) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("kind://check", doc); err != nil {
		return err
	}
	_, err = c.Compile("kind://check")
	return err
}

// flatten turns a nested validation error into one line: "at /path: message; ...".
func flatten(err error) string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	var parts []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			parts = append(parts, fmt.Sprintf("at %s: %s", loc, e.ErrorKind.LocalizedString(printer)))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return strings.Join(parts, "; ")
}
