// Package events validates, redacts, stores and lists events.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/events/redact"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrNotFound is returned for unknown event ids.
var ErrNotFound = errors.New("event not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid event")

// Limits.
const (
	MaxTitle       = 200
	MaxBody        = 4000
	MaxDataSize    = 256 * 1024
	MaxLimit       = 200
	MaxActions     = 3
	MaxActionLabel = 40
	MaxActionURL   = 2048
)

// Action is a button attached to an event: on the phone it appears on the
// notification and in the event detail; on the web in the event detail.
type Action struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// blockedSchemes are never allowed in action URLs.
var blockedSchemes = map[string]bool{"javascript": true, "data": true, "file": true, "vbscript": true}

// validateActions normalises and checks a list of actions.
func validateActions(in []Action) ([]Action, error) {
	if len(in) > MaxActions {
		return nil, fmt.Errorf("%w: at most %d actions are allowed", ErrInvalid, MaxActions)
	}
	out := make([]Action, 0, len(in))
	for _, a := range in {
		a.Label = strings.TrimSpace(a.Label)
		a.URL = strings.TrimSpace(a.URL)
		if a.Label == "" {
			return nil, fmt.Errorf("%w: action label is required", ErrInvalid)
		}
		if len(a.Label) > MaxActionLabel {
			return nil, fmt.Errorf("%w: action label must be %d characters or fewer", ErrInvalid, MaxActionLabel)
		}
		if a.URL == "" {
			return nil, fmt.Errorf("%w: action url is required", ErrInvalid)
		}
		if len(a.URL) > MaxActionURL {
			return nil, fmt.Errorf("%w: action url must be %d characters or fewer", ErrInvalid, MaxActionURL)
		}
		u, err := url.Parse(a.URL)
		if err != nil || u.Scheme == "" {
			return nil, fmt.Errorf("%w: action url must be absolute (https://... or an app scheme)", ErrInvalid)
		}
		if blockedSchemes[strings.ToLower(u.Scheme)] {
			return nil, fmt.Errorf("%w: action url scheme %q is not allowed", ErrInvalid, u.Scheme)
		}
		out = append(out, a)
	}
	return out, nil
}

// Input is the inbound event envelope.
type Input struct {
	ExternalID  string          `json:"external_id"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Level       string          `json:"level"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Fingerprint string          `json:"fingerprint"`
	OccurredAt  string          `json:"occurred_at"`
	Data        json.RawMessage `json:"data"`
	Actions     []Action        `json:"actions"`
	// Resolve marks a recovery: it resolves the open alert with the same
	// fingerprint in the project (and is stored as a success event by default).
	Resolve bool `json:"resolve"`
}

// Event is a stored event.
type Event struct {
	ID          string          `json:"id"`
	ExternalID  string          `json:"external_id,omitempty"`
	ProjectID   string          `json:"project_id"`
	ProjectName string          `json:"project_name"`
	ProjectSlug string          `json:"project_slug"`
	ProjectIcon string          `json:"project_icon"`
	ClientID    string          `json:"client_id"`
	ClientName  string          `json:"client_name"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	Level       string          `json:"level"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	Fingerprint string          `json:"fingerprint"`
	Data        json.RawMessage `json:"data"`
	Actions     []Action        `json:"actions,omitempty"`
	OccurredAt  string          `json:"occurred_at"`
	CreatedAt   string          `json:"created_at"`
	// SilenceID is set when a silence rule stopped this event from being pushed.
	SilenceID string `json:"silence_id,omitempty"`
	Silenced  bool   `json:"silenced"`
	// AlertID is the alert group this event belongs to ("" for info/success
	// events that neither opened nor resolved an alert).
	AlertID string `json:"alert_id,omitempty"`
	// Route says why the event did or did not push.
	Route string `json:"route,omitempty"`
	// Group is set in grouped listings for events that carry a fingerprint: this
	// event is the latest of Count occurrences sharing (project, fingerprint).
	Group *GroupInfo `json:"group,omitempty"`
}

// GroupInfo summarises the occurrences behind a grouped row.
type GroupInfo struct {
	Count     int    `json:"count"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// Filter narrows List.
type Filter struct {
	ProjectID   string
	ClientID    string // client id or slug
	AlertID     string
	Level       string
	Source      string
	Fingerprint string
	Before      string // cursor: id of the last event seen
	Limit       int
	Silenced    *bool  // nil = any
	Since       string // created_at >= (TimeLayout or RFC 3339)
	Until       string // created_at < (TimeLayout or RFC 3339)
	// Query is a case-insensitive substring match over title, body, source,
	// fingerprint and the data payload.
	Query string
	// Grouped collapses events sharing a non-empty fingerprint within a project
	// into one row (the latest occurrence) annotated with Group.
	Grouped bool
}

// Page is a page of events plus the cursor for the next page ("" when exhausted).
type Page struct {
	Events     []Event `json:"events"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// Store persists events.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Validate normalises in, returning the redacted data ready for storage.
func Validate(in Input, r *redact.Redactor) (Input, []byte, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return in, nil, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if len(in.Title) > MaxTitle {
		return in, nil, fmt.Errorf("%w: title must be %d characters or fewer", ErrInvalid, MaxTitle)
	}
	if len(in.Body) > MaxBody {
		return in, nil, fmt.Errorf("%w: body must be %d characters or fewer", ErrInvalid, MaxBody)
	}
	if in.Level == "" {
		in.Level = levels.Info
		if in.Resolve {
			in.Level = levels.Success
		}
	}
	in.Level = strings.ToLower(in.Level)
	if !levels.Valid(in.Level) {
		return in, nil, fmt.Errorf("%w: level must be one of %s", ErrInvalid, strings.Join(levels.All, ", "))
	}
	for _, f := range []*string{&in.ExternalID, &in.Source, &in.Type, &in.Fingerprint} {
		*f = strings.TrimSpace(*f)
		if len(*f) > 200 {
			return in, nil, fmt.Errorf("%w: external_id, source, type and fingerprint must be 200 characters or fewer", ErrInvalid)
		}
	}
	if in.OccurredAt == "" {
		in.OccurredAt = ids.Now()
	} else {
		t, err := ids.Parse(in.OccurredAt)
		if err != nil {
			return in, nil, fmt.Errorf("%w: occurred_at must be an RFC 3339 timestamp", ErrInvalid)
		}
		in.OccurredAt = ids.Format(t)
	}

	actions, err := validateActions(in.Actions)
	if err != nil {
		return in, nil, err
	}
	in.Actions = actions

	data := []byte("{}")
	if len(in.Data) > 0 && string(in.Data) != "null" {
		if len(in.Data) > MaxDataSize {
			return in, nil, fmt.Errorf("%w: data must be %d bytes or fewer", ErrInvalid, MaxDataSize)
		}
		var v map[string]any
		if err := json.Unmarshal(in.Data, &v); err != nil {
			return in, nil, fmt.Errorf("%w: data must be a JSON object", ErrInvalid)
		}
		var err error
		data, err = json.Marshal(r.Apply(v))
		if err != nil {
			return in, nil, err
		}
	}
	return in, data, nil
}

// Create validates, redacts and stores an event for projectID.
func (s *Store) Create(ctx context.Context, projectID string, in Input, r *redact.Redactor) (Event, error) {
	in, data, err := Validate(in, r)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		ID:          ids.New("evt"),
		ExternalID:  in.ExternalID,
		ProjectID:   projectID,
		Source:      in.Source,
		Type:        in.Type,
		Level:       in.Level,
		Title:       in.Title,
		Body:        in.Body,
		Fingerprint: in.Fingerprint,
		Data:        data,
		Actions:     in.Actions,
		OccurredAt:  in.OccurredAt,
		CreatedAt:   ids.Now(),
	}
	actionsJSON := "[]"
	if len(e.Actions) > 0 {
		b, err := json.Marshal(e.Actions)
		if err != nil {
			return Event{}, err
		}
		actionsJSON = string(b)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO events
		(id, external_id, project_id, source, type, level, title, body, fingerprint, payload_json, actions_json, occurred_at, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, nullable(e.ExternalID), e.ProjectID, e.Source, e.Type, e.Level, e.Title, e.Body, e.Fingerprint, string(e.Data), actionsJSON, e.OccurredAt, e.CreatedAt)
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, e.ID)
}

const selectCols = `e.id, COALESCE(e.external_id, ''), e.project_id, p.name, p.slug, p.icon, COALESCE(p.client_id, ''), COALESCE(c.name, ''), e.source, e.type, e.level, e.title, e.body, e.fingerprint, e.payload_json, e.actions_json, e.occurred_at, e.created_at, COALESCE(e.silence_id, ''), COALESCE(e.group_id, ''), e.route`

const fromClause = ` FROM events e JOIN projects p ON p.id = e.project_id LEFT JOIN clients c ON c.id = p.client_id`

func scan(row interface{ Scan(...any) error }, extra ...any) (Event, error) {
	var e Event
	var data, actions string
	dest := []any{&e.ID, &e.ExternalID, &e.ProjectID, &e.ProjectName, &e.ProjectSlug, &e.ProjectIcon, &e.ClientID, &e.ClientName, &e.Source, &e.Type, &e.Level, &e.Title, &e.Body, &e.Fingerprint, &data, &actions, &e.OccurredAt, &e.CreatedAt, &e.SilenceID, &e.AlertID, &e.Route}
	dest = append(dest, extra...)
	err := row.Scan(dest...)
	e.Data = json.RawMessage(data)
	e.Silenced = e.SilenceID != ""
	if actions != "" && actions != "[]" {
		_ = json.Unmarshal([]byte(actions), &e.Actions)
	}
	return e, err
}

// Get returns one event.
func (s *Store) Get(ctx context.Context, id string) (Event, error) {
	e, err := scan(s.db.QueryRowContext(ctx, `SELECT `+selectCols+fromClause+` WHERE e.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	return e, err
}

// filterClause builds the WHERE fragments for f (cursor excluded) against
// the events table aliased as alias.
func filterClause(f Filter, alias string) (where []string, args []any) {
	a := alias + "."
	if f.ProjectID != "" {
		// p is the joined projects row; only the outer query has it.
		if alias == "e" {
			where = append(where, "("+a+"project_id = ? OR p.slug = ?)")
			args = append(args, f.ProjectID, f.ProjectID)
		} else {
			where = append(where, "("+a+"project_id = ? OR "+a+"project_id IN (SELECT id FROM projects WHERE slug = ?))")
			args = append(args, f.ProjectID, f.ProjectID)
		}
	}
	if f.ClientID != "" {
		where = append(where, a+"project_id IN (SELECT pp.id FROM projects pp JOIN clients cc ON cc.id = pp.client_id WHERE cc.id = ? OR cc.slug = ?)")
		args = append(args, f.ClientID, f.ClientID)
	}
	if f.AlertID != "" {
		where = append(where, a+"group_id = ?")
		args = append(args, f.AlertID)
	}
	if f.Level != "" {
		where = append(where, a+"level = ?")
		args = append(args, f.Level)
	}
	if f.Source != "" {
		where = append(where, a+"source = ?")
		args = append(args, f.Source)
	}
	if f.Fingerprint != "" {
		where = append(where, a+"fingerprint = ?")
		args = append(args, f.Fingerprint)
	}
	if f.Silenced != nil {
		if *f.Silenced {
			where = append(where, a+"silence_id IS NOT NULL")
		} else {
			where = append(where, a+"silence_id IS NULL")
		}
	}
	if f.Since != "" {
		where = append(where, a+"created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where = append(where, a+"created_at < ?")
		args = append(args, f.Until)
	}
	if f.Query != "" {
		q := strings.ToLower(strings.TrimSpace(f.Query))
		where = append(where, "(instr(lower("+a+"title), ?) > 0 OR instr(lower("+a+"body), ?) > 0 OR instr(lower("+a+"source), ?) > 0 OR instr(lower("+a+"fingerprint), ?) > 0 OR instr(lower("+a+"payload_json), ?) > 0)")
		args = append(args, q, q, q, q, q)
	}
	return where, args
}

// normaliseTime accepts TimeLayout or RFC 3339 and returns TimeLayout, so
// string comparison against created_at is chronological.
func normaliseTime(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, err := ids.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%w: timestamps must be RFC 3339", ErrInvalid)
	}
	return ids.Format(t), nil
}

// List returns events newest-first using keyset pagination on (created_at, id).
//
// With f.Grouped, events that share a non-empty fingerprint within a project
// collapse into their latest occurrence (respecting the other filters), and
// each such row carries Group with the occurrence count and first/last seen.
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	var err error
	if f.Since, err = normaliseTime(f.Since); err != nil {
		return Page{}, err
	}
	if f.Until, err = normaliseTime(f.Until); err != nil {
		return Page{}, err
	}
	where, args := filterClause(f, "e")
	where = append([]string{"1=1"}, where...)

	cols := selectCols
	if f.Grouped {
		// The same filters apply inside the group so that, say, level=error
		// shows the latest *error* of a fingerprint and counts only errors.
		gw, ga := filterClause(f, "x")
		gw = append([]string{"x.project_id = e.project_id", "x.fingerprint = e.fingerprint"}, gw...)
		g := strings.Join(gw, " AND ")
		where = append(where, "(e.fingerprint = '' OR e.id = (SELECT x.id FROM events x WHERE "+g+" ORDER BY x.created_at DESC, x.id DESC LIMIT 1))")
		// Pre-pend the group-subquery args: the correlated selects come first in the SQL text.
		cols += `, CASE WHEN e.fingerprint = '' THEN 0 ELSE (SELECT COUNT(*) FROM events x WHERE ` + g + `) END,
			CASE WHEN e.fingerprint = '' THEN '' ELSE (SELECT MIN(x.created_at) FROM events x WHERE ` + g + `) END,
			CASE WHEN e.fingerprint = '' THEN '' ELSE (SELECT MAX(x.created_at) FROM events x WHERE ` + g + `) END`
		var all []any
		all = append(all, ga...)
		all = append(all, ga...)
		all = append(all, ga...)
		all = append(all, args...)
		all = append(all, ga...)
		args = all
	}
	if f.Before != "" {
		var createdAt string
		err := s.db.QueryRowContext(ctx, `SELECT created_at FROM events WHERE id = ?`, f.Before).Scan(&createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return Page{}, fmt.Errorf("%w: unknown cursor", ErrInvalid)
		}
		if err != nil {
			return Page{}, err
		}
		where = append(where, "(e.created_at < ? OR (e.created_at = ? AND e.id < ?))")
		args = append(args, createdAt, createdAt, f.Before)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+fromClause+` WHERE `+strings.Join(where, " AND ")+` ORDER BY e.created_at DESC, e.id DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Events: []Event{}}
	for rows.Next() {
		var e Event
		if f.Grouped {
			var g GroupInfo
			e, err = scan(rows, &g.Count, &g.FirstSeen, &g.LastSeen)
			if err == nil && e.Fingerprint != "" {
				e.Group = &g
			}
		} else {
			e, err = scan(rows)
		}
		if err != nil {
			return Page{}, err
		}
		page.Events = append(page.Events, e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Events) > f.Limit {
		page.Events = page.Events[:f.Limit]
		page.NextCursor = page.Events[len(page.Events)-1].ID
	}
	return page, nil
}

// SetSilence records which rule silenced an event.
func (s *Store) SetSilence(ctx context.Context, eventID, silenceID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET silence_id = ? WHERE id = ?`, silenceID, eventID)
	return err
}

// SetRoute records why an event did or did not push.
func (s *Store) SetRoute(ctx context.Context, eventID, route string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET route = ? WHERE id = ?`, route, eventID)
	return err
}

// ClearSilence removes the silenced flag from an event.
func (s *Store) ClearSilence(ctx context.Context, eventID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE events SET silence_id = NULL WHERE id = ?`, eventID)
	return err
}

// CountSilenced returns how many stored events were silenced.
func (s *Store) CountSilenced(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE silence_id IS NOT NULL`).Scan(&n)
	return n, err
}

// Count returns the total number of stored events.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}

// Prune deletes events created more than days ago. days <= 0 disables pruning.
func (s *Store) Prune(ctx context.Context, days int, now time.Time) (int64, error) {
	if days <= 0 {
		return 0, nil
	}
	cutoff := ids.Format(now.Add(-time.Duration(days) * 24 * time.Hour))
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
