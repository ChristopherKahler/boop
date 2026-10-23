// Package alerts groups problem events into alerts and tracks their
// acknowledge and resolve times.
//
// An alert is one problem: the warning/error/critical events that share a
// fingerprint within a project, from the first occurrence until the alert is
// resolved (by a recovery event, the phone, the web UI or the API). Repeats
// while it is open are counted on the same alert. An occurrence after it was
// resolved opens a new alert, so each alert is one incident with its own
// acknowledge and resolve times. Events without a fingerprint each form their
// own alert.
package alerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/database"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrNotFound is returned for unknown alert ids.
var ErrNotFound = errors.New("alert not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid alert request")

// ErrConflict is returned when reopening an alert whose problem already has a
// newer open alert.
var ErrConflict = errors.New("a newer alert for this problem is already open")

// Statuses, derived from the acknowledge and resolve times.
const (
	StatusOpen         = "open"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"
	// StatusActive is a filter: open or acknowledged.
	StatusActive = "active"
)

// Who resolved an alert, besides a person ("web", "phone", "api").
const ResolvedByRecovery = "recovery"

// Change is what an incoming event did to its alert.
type Change string

// Changes.
const (
	None      Change = ""          // not an alert: info/success, or a recovery with nothing open
	Opened    Change = "opened"    // first occurrence: a new alert
	Repeated  Change = "repeated"  // another occurrence of an open alert at the same or a lower level
	Escalated Change = "escalated" // another occurrence that raised the alert's level
	Resolved  Change = "resolved"  // a recovery event resolved the open alert
)

// Alert is one incident.
type Alert struct {
	ID             string  `json:"id"`
	ProjectID      string  `json:"project_id"`
	ProjectName    string  `json:"project_name"`
	ProjectSlug    string  `json:"project_slug"`
	ProjectIcon    string  `json:"project_icon"`
	ClientID       string  `json:"client_id"`
	ClientName     string  `json:"client_name"`
	Fingerprint    string  `json:"fingerprint"`
	Source         string  `json:"source"`
	Title          string  `json:"title"`
	Level          string  `json:"level"`
	Status         string  `json:"status"`
	Count          int     `json:"count"`
	FirstSeen      string  `json:"first_seen"`
	LastSeen       string  `json:"last_seen"`
	LastEventID    string  `json:"last_event_id,omitempty"`
	NotifiedAt     *string `json:"notified_at"`
	AcknowledgedAt *string `json:"acknowledged_at"`
	AcknowledgedBy string  `json:"acknowledged_by,omitempty"`
	ResolvedAt     *string `json:"resolved_at"`
	ResolvedBy     string  `json:"resolved_by,omitempty"`
	ResolveEventID string  `json:"resolve_event_id,omitempty"`
}

// Attachment reports which alert an event joined and how.
type Attachment struct {
	Alert  *Alert
	Change Change
	// PreviousLevel is the alert's level before this event (for escalations).
	PreviousLevel string
}

// Filter narrows List.
type Filter struct {
	Status    string // open | acknowledged | resolved | active | "" (any)
	ClientID  string // id or slug
	ProjectID string // id or slug
	Level     string
	Before    string // cursor: id of the last alert seen
	Limit     int
}

// Page is a page of alerts.
type Page struct {
	Alerts     []Alert `json:"alerts"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// Store persists alerts.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `g.id, g.project_id, p.name, p.slug, p.icon, COALESCE(p.client_id, ''), COALESCE(c.name, ''),
	g.fingerprint, g.source, g.title, g.level, g.event_count, g.first_seen, g.last_seen, COALESCE(g.last_event_id, ''),
	g.notified_at, g.acknowledged_at, g.acknowledged_by, g.resolved_at, g.resolved_by, COALESCE(g.resolve_event_id, '')`

const from = ` FROM alert_groups g JOIN projects p ON p.id = g.project_id LEFT JOIN clients c ON c.id = p.client_id`

func scan(row interface{ Scan(...any) error }) (Alert, error) {
	var a Alert
	var notified, acked, resolved sql.NullString
	err := row.Scan(&a.ID, &a.ProjectID, &a.ProjectName, &a.ProjectSlug, &a.ProjectIcon, &a.ClientID, &a.ClientName,
		&a.Fingerprint, &a.Source, &a.Title, &a.Level, &a.Count, &a.FirstSeen, &a.LastSeen, &a.LastEventID,
		&notified, &acked, &a.AcknowledgedBy, &resolved, &a.ResolvedBy, &a.ResolveEventID)
	a.NotifiedAt = nullString(notified)
	a.AcknowledgedAt = nullString(acked)
	a.ResolvedAt = nullString(resolved)
	switch {
	case a.ResolvedAt != nil:
		a.Status = StatusResolved
	case a.AcknowledgedAt != nil:
		a.Status = StatusAcknowledged
	default:
		a.Status = StatusOpen
	}
	return a, err
}

func nullString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// Key is the grouping key for an event: its fingerprint, or its own id when it
// has none (so it forms an alert of its own).
func Key(e events.Event) string {
	if e.Fingerprint != "" {
		return e.Fingerprint
	}
	return "event:" + e.ID
}

// Attach files a stored event under its alert. Problem events (warning and
// above) open or join the open alert for their key; a recovery event (resolve)
// resolves it. Other events are left alone (Change None).
func (s *Store) Attach(ctx context.Context, e events.Event, resolve bool) (Attachment, error) {
	if !resolve && !levels.Problem(e.Level) {
		return Attachment{}, nil
	}
	key := Key(e)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, err
	}
	defer tx.Rollback()

	var id, level string
	err = tx.QueryRowContext(ctx, `SELECT id, level FROM alert_groups WHERE project_id = ? AND group_key = ? AND resolved_at IS NULL`, e.ProjectID, key).Scan(&id, &level)
	open := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, err
	}
	now := ids.Now()
	att := Attachment{}
	switch {
	case resolve && !open:
		return Attachment{}, nil
	case resolve:
		_, err = tx.ExecContext(ctx, `UPDATE alert_groups SET resolved_at = ?, resolved_by = ?, resolve_event_id = ?, updated_at = ? WHERE id = ?`,
			e.CreatedAt, ResolvedByRecovery, e.ID, now, id)
		att.Change, att.PreviousLevel = Resolved, level
	case !open:
		id = ids.New("alr")
		_, err = tx.ExecContext(ctx, `INSERT INTO alert_groups (id, project_id, group_key, fingerprint, source, title, level, event_count, first_seen, last_seen, last_event_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, e.ProjectID, key, e.Fingerprint, e.Source, e.Title, e.Level, 1, e.CreatedAt, e.CreatedAt, e.ID, now, now)
		att.Change = Opened
	default:
		next := levels.Max(level, e.Level)
		_, err = tx.ExecContext(ctx, `UPDATE alert_groups SET event_count = event_count + 1, last_seen = ?, last_event_id = ?, title = ?, level = ?, updated_at = ? WHERE id = ?`,
			e.CreatedAt, e.ID, e.Title, next, now, id)
		att.Change, att.PreviousLevel = Repeated, level
		if next != level {
			att.Change = Escalated
		}
	}
	if err != nil {
		if database.IsUniqueViolation(err) {
			// Another writer opened this alert between our read and insert;
			// the single-connection pool makes this unreachable today.
			return Attachment{}, fmt.Errorf("alerts: concurrent open for %s: %w", key, err)
		}
		return Attachment{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE events SET group_id = ? WHERE id = ?`, id, e.ID); err != nil {
		return Attachment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attachment{}, err
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return Attachment{}, err
	}
	att.Alert = &a
	return att, nil
}

// Get returns one alert.
func (s *Store) Get(ctx context.Context, id string) (Alert, error) {
	a, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` WHERE g.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	return a, err
}

// List returns alerts, most recently active first.
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > events.MaxLimit {
		f.Limit = events.MaxLimit
	}
	where := []string{"1=1"}
	var args []any
	switch f.Status {
	case "":
	case StatusOpen:
		where = append(where, "g.resolved_at IS NULL AND g.acknowledged_at IS NULL")
	case StatusAcknowledged:
		where = append(where, "g.resolved_at IS NULL AND g.acknowledged_at IS NOT NULL")
	case StatusActive:
		where = append(where, "g.resolved_at IS NULL")
	case StatusResolved:
		where = append(where, "g.resolved_at IS NOT NULL")
	default:
		return Page{}, fmt.Errorf("%w: status must be open, acknowledged, active or resolved", ErrInvalid)
	}
	if f.ClientID != "" {
		where = append(where, "(c.id = ? OR c.slug = ?)")
		args = append(args, f.ClientID, f.ClientID)
	}
	if f.ProjectID != "" {
		where = append(where, "(p.id = ? OR p.slug = ?)")
		args = append(args, f.ProjectID, f.ProjectID)
	}
	if f.Level != "" {
		if !levels.Valid(f.Level) {
			return Page{}, fmt.Errorf("%w: level must be one of %s", ErrInvalid, strings.Join(levels.All, ", "))
		}
		where = append(where, "g.level = ?")
		args = append(args, f.Level)
	}
	if f.Before != "" {
		var lastSeen string
		err := s.db.QueryRowContext(ctx, `SELECT last_seen FROM alert_groups WHERE id = ?`, f.Before).Scan(&lastSeen)
		if errors.Is(err, sql.ErrNoRows) {
			return Page{}, fmt.Errorf("%w: unknown cursor", ErrInvalid)
		}
		if err != nil {
			return Page{}, err
		}
		where = append(where, "(g.last_seen < ? OR (g.last_seen = ? AND g.id < ?))")
		args = append(args, lastSeen, lastSeen, f.Before)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY g.last_seen DESC, g.id DESC LIMIT ?`, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Alerts: []Alert{}}
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return Page{}, err
		}
		page.Alerts = append(page.Alerts, a)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Alerts) > f.Limit {
		page.Alerts = page.Alerts[:f.Limit]
		page.NextCursor = page.Alerts[len(page.Alerts)-1].ID
	}
	return page, nil
}

// Counts returns how many alerts are open and acknowledged (not resolved).
func (s *Store) Counts(ctx context.Context) (open, acknowledged int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN acknowledged_at IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN acknowledged_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM alert_groups WHERE resolved_at IS NULL`).Scan(&open, &acknowledged)
	return
}

func clipBy(by string) string {
	by = strings.TrimSpace(by)
	if len(by) > 80 {
		by = by[:80]
	}
	return by
}

// Acknowledge marks an alert as seen. Acknowledging twice keeps the first
// time; acknowledging a resolved alert is a no-op.
func (s *Store) Acknowledge(ctx context.Context, id, by string) (Alert, error) {
	now := ids.Now()
	if _, err := s.db.ExecContext(ctx, `UPDATE alert_groups SET acknowledged_at = ?, acknowledged_by = ?, updated_at = ?
		WHERE id = ? AND acknowledged_at IS NULL AND resolved_at IS NULL`, now, clipBy(by), now, id); err != nil {
		return Alert{}, err
	}
	return s.Get(ctx, id)
}

// Resolve closes an alert by hand. Resolving twice keeps the first time.
func (s *Store) Resolve(ctx context.Context, id, by string) (Alert, error) {
	now := ids.Now()
	if _, err := s.db.ExecContext(ctx, `UPDATE alert_groups SET resolved_at = ?, resolved_by = ?, updated_at = ?
		WHERE id = ? AND resolved_at IS NULL`, now, clipBy(by), now, id); err != nil {
		return Alert{}, err
	}
	return s.Get(ctx, id)
}

// Reopen undoes a resolve, for when it was premature. It fails with
// ErrConflict when the same problem has since opened a newer alert.
func (s *Store) Reopen(ctx context.Context, id string) (Alert, error) {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_groups SET resolved_at = NULL, resolved_by = '', resolve_event_id = NULL, updated_at = ?
		WHERE id = ? AND resolved_at IS NOT NULL`, ids.Now(), id)
	if database.IsUniqueViolation(err) {
		return Alert{}, ErrConflict
	}
	if err != nil {
		return Alert{}, err
	}
	return s.Get(ctx, id)
}

// MarkNotified records the first successful push for an alert.
func (s *Store) MarkNotified(ctx context.Context, id, at string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_groups SET notified_at = COALESCE(notified_at, ?) WHERE id = ?`, at, id)
	return err
}
