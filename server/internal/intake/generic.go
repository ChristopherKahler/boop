package intake

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Mapping turns any JSON webhook into events without code. Fields are
// templates: "{{path}}" is replaced by the value at that dotted path in the
// payload ("alert.labels.severity", "items.0.name"); "{{path|fallback}}"
// uses the fallback when the value is missing. Example:
//
//	{
//	  "title": "{{alert.name}} on {{host}}",
//	  "body": "{{description}}",
//	  "level": "{{severity}}",
//	  "level_map": {"p1": "critical", "p2": "error", "p3": "warning"},
//	  "fingerprint": "{{alert.id}}",
//	  "resolve_when": {"field": "status", "equals": ["resolved", "ok"]},
//	  "url": "{{link}}"
//	}
//
// With "items" set to the path of an array, each element becomes its own
// event (paths then read the element first, and "$.path" reads the whole
// payload), which suits services that batch alerts in one request.
type Mapping struct {
	Title        string            `json:"title"`
	Body         string            `json:"body,omitempty"`
	Level        string            `json:"level,omitempty"`
	LevelMap     map[string]string `json:"level_map,omitempty"`
	DefaultLevel string            `json:"default_level,omitempty"`
	Fingerprint  string            `json:"fingerprint,omitempty"`
	Source       string            `json:"source,omitempty"`
	ResolveWhen  *Condition        `json:"resolve_when,omitempty"`
	IgnoreWhen   *Condition        `json:"ignore_when,omitempty"`
	URL          string            `json:"url,omitempty"`
	URLLabel     string            `json:"url_label,omitempty"`
	Items        string            `json:"items,omitempty"`
}

// Condition matches when the value at Field equals any of Equals (case-insensitive).
type Condition struct {
	Field  string   `json:"field"`
	Equals []string `json:"equals"`
}

// Validate checks a mapping can produce events.
func (m Mapping) Validate() error {
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("title is required")
	}
	if m.DefaultLevel != "" && !levels.Valid(m.DefaultLevel) {
		return fmt.Errorf("default_level %q is not a level", m.DefaultLevel)
	}
	for k, v := range m.LevelMap {
		if !levels.Valid(v) {
			return fmt.Errorf("level_map[%q] = %q is not a level (use %s)", k, v, strings.Join(levels.All, ", "))
		}
	}
	for _, c := range []*Condition{m.ResolveWhen, m.IgnoreWhen} {
		if c != nil && (c.Field == "" || len(c.Equals) == 0) {
			return errors.New("resolve_when and ignore_when need a field and at least one value")
		}
	}
	return nil
}

func mapGeneric(r Request) (Result, error) {
	var m Mapping
	if err := json.Unmarshal(r.Mapping, &m); err != nil || m.Validate() != nil {
		return Result{}, errors.New("this intake's mapping is invalid; fix it in the project's intake settings")
	}
	if r.Secret != "" && !validHMAC(r.Secret, r.Body, firstNonEmpty(r.Header.Get("X-Signature-256"), r.Header.Get("X-Signature")), "sha256=") {
		return Result{}, ErrSignature
	}
	var root any
	if err := json.Unmarshal(r.Body, &root); err != nil {
		return Result{}, ErrPayload
	}
	items := []any{root}
	if m.Items != "" {
		list, ok := lookup(root, m.Items).([]any)
		if !ok {
			return ignored("no items at " + m.Items)
		}
		items = list
	}
	var out []events.Input
	skipped := 0
	for _, item := range items {
		if m.IgnoreWhen != nil && m.IgnoreWhen.matches(root, item) {
			skipped++
			continue
		}
		in := events.Input{
			Source:      firstNonEmpty(render(m.Source, root, item), "webhook"),
			Title:       render(m.Title, root, item),
			Body:        render(m.Body, root, item),
			Fingerprint: render(m.Fingerprint, root, item),
			Level:       m.level(render(m.Level, root, item)),
		}
		if in.Fingerprint == "" {
			in.Fingerprint = "webhook:" + strings.ToLower(in.Title)
		}
		if m.ResolveWhen != nil && m.ResolveWhen.matches(root, item) {
			in.Resolve, in.Level = true, levels.Success
		}
		if u := render(m.URL, root, item); u != "" {
			in.Actions = []events.Action{{Label: firstNonEmpty(m.URLLabel, "Open"), URL: u}}
		}
		if obj, ok := item.(map[string]any); ok {
			in.Data = data(obj)
		}
		out = append(out, in)
	}
	if len(out) == 0 {
		return ignored(fmt.Sprintf("ignore_when matched %d item(s)", skipped))
	}
	return Result{Events: out}, nil
}

func (m Mapping) level(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for k, l := range m.LevelMap {
		if strings.EqualFold(k, v) {
			return l
		}
	}
	if levels.Valid(v) {
		return v
	}
	if v == "fatal" {
		return levels.Critical
	}
	if m.DefaultLevel != "" {
		return m.DefaultLevel
	}
	return levels.Error
}

func (c *Condition) matches(root, item any) bool {
	v := strings.TrimSpace(stringify(resolvePath(root, item, c.Field)))
	for _, e := range c.Equals {
		if strings.EqualFold(v, strings.TrimSpace(e)) {
			return true
		}
	}
	return false
}

var placeholder = regexp.MustCompile(`\{\{\s*([^}|]+?)\s*(?:\|([^}]*))?\}\}`)

// render fills a template's {{path}} placeholders from the payload.
func render(tmpl string, root, item any) string {
	if tmpl == "" {
		return ""
	}
	out := placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		g := placeholder.FindStringSubmatch(m)
		v := stringify(resolvePath(root, item, g[1]))
		if v == "" {
			return g[2]
		}
		return v
	})
	return strings.TrimSpace(out)
}

// resolvePath reads "$.a.b" from the whole payload, otherwise "a.b" from the
// current item first and then from the whole payload.
func resolvePath(root, item any, path string) any {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "$.") {
		return lookup(root, strings.TrimPrefix(path, "$."))
	}
	if v := lookup(item, path); v != nil {
		return v
	}
	return lookup(root, path)
}

func lookup(v any, path string) any {
	if path == "" || path == "$" {
		return v
	}
	for _, seg := range strings.Split(path, ".") {
		switch cur := v.(type) {
		case map[string]any:
			v = cur[seg]
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(cur) {
				return nil
			}
			v = cur[i]
		default:
			return nil
		}
		if v == nil {
			return nil
		}
	}
	return v
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
