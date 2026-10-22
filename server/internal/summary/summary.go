// Package summary builds the monthly report for one client: how many alerts,
// what broke, how fast each was acknowledged and resolved, and what
// recovered on its own. It is what an operator shows a client to account for
// the monitoring.
package summary

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrInvalid wraps bad month or timezone input.
var ErrInvalid = errors.New("invalid summary request")

// Summary is one client's month.
type Summary struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	ClientSlug string `json:"client_slug"`
	Month      string `json:"month"` // 2026-10
	MonthLabel string `json:"month_label"`
	Timezone   string `json:"timezone"`
	From       string `json:"from"` // UTC bounds of the month in Timezone
	To         string `json:"to"`

	Totals         Totals         `json:"totals"`
	ByLevel        []LevelCount   `json:"by_level"`        // alerts opened, by level
	EventsByLevel  []LevelCount   `json:"events_by_level"` // every event received, by level
	Projects       []ProjectCount `json:"projects"`
	Acknowledge    Durations      `json:"time_to_acknowledge"`
	Resolve        Durations      `json:"time_to_resolve"`
	Notify         Durations      `json:"time_to_notify"`
	Alerts         []Alert        `json:"alerts"`     // what broke: alerts opened this month, oldest first
	Recoveries     []Alert        `json:"recoveries"` // alerts that recovered on their own this month
	CarriedOver    int            `json:"carried_over"`
	PreviousMonth  string         `json:"previous_month"`
	NextMonth      string         `json:"next_month"`
	GeneratedAt    string         `json:"generated_at"`
	RetentionNotes string         `json:"retention_note,omitempty"`
}

// Totals are the headline numbers.
type Totals struct {
	Alerts         int `json:"alerts"`
	Resolved       int `json:"resolved"`
	Recovered      int `json:"recovered"` // resolved by a recovery event
	ResolvedByHand int `json:"resolved_by_hand"`
	StillOpen      int `json:"still_open"`
	Acknowledged   int `json:"acknowledged"`
	Occurrences    int `json:"occurrences"` // problem events across the month's alerts
	Events         int `json:"events"`      // every event received in the month
	Pushes         int `json:"pushes"`      // push notifications delivered
}

// LevelCount is a count for one level.
type LevelCount struct {
	Level string `json:"level"`
	Count int    `json:"count"`
}

// ProjectCount is the alerts opened for one project.
type ProjectCount struct {
	Project string `json:"project"`
	Alerts  int    `json:"alerts"`
	Open    int    `json:"open"`
}

// Durations summarises a set of response times, in seconds.
type Durations struct {
	Count   int     `json:"count"`
	Median  float64 `json:"median_seconds"`
	Average float64 `json:"average_seconds"`
	Fastest float64 `json:"fastest_seconds"`
	Slowest float64 `json:"slowest_seconds"`
}

// Alert is one row of "what broke".
type Alert struct {
	ID             string   `json:"id"`
	Project        string   `json:"project"`
	Title          string   `json:"title"`
	Level          string   `json:"level"`
	Source         string   `json:"source"`
	FirstSeen      string   `json:"first_seen"`
	LastSeen       string   `json:"last_seen"`
	Count          int      `json:"count"`
	Status         string   `json:"status"`
	AcknowledgedAt *string  `json:"acknowledged_at"`
	AcknowledgedBy string   `json:"acknowledged_by,omitempty"`
	ResolvedAt     *string  `json:"resolved_at"`
	ResolvedBy     string   `json:"resolved_by,omitempty"`
	ToAcknowledge  *float64 `json:"time_to_acknowledge_seconds"`
	ToResolve      *float64 `json:"time_to_resolve_seconds"`
	ToNotify       *float64 `json:"time_to_notify_seconds"`
}

// MonthBounds parses "2006-01" in loc and returns its start and the next month's start.
func MonthBounds(month string, loc *time.Location) (time.Time, time.Time, error) {
	t, err := time.ParseInLocation("2006-01", month, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: month must look like 2026-10", ErrInvalid)
	}
	return t, t.AddDate(0, 1, 0), nil
}

// Build computes the summary for clientID (an id or slug) and month in loc.
func Build(ctx context.Context, db *sql.DB, clientID, month string, loc *time.Location, now time.Time) (Summary, error) {
	from, to, err := MonthBounds(month, loc)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Month: month, MonthLabel: from.Format("January 2006"), Timezone: loc.String(),
		From: ids.Format(from), To: ids.Format(to), GeneratedAt: ids.Format(now),
		PreviousMonth: from.AddDate(0, -1, 0).Format("2006-01"), NextMonth: to.Format("2006-01")}
	err = db.QueryRowContext(ctx, `SELECT id, name, slug FROM clients WHERE id = ? OR slug = ?`, clientID, clientID).Scan(&s.ClientID, &s.ClientName, &s.ClientSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, fmt.Errorf("client not found: %w", sql.ErrNoRows)
	}
	if err != nil {
		return Summary{}, err
	}

	// What broke: alerts that opened this month.
	rows, err := db.QueryContext(ctx, `SELECT g.id, p.name, g.title, g.level, g.source, g.first_seen, g.last_seen, g.event_count,
		g.acknowledged_at, g.acknowledged_by, g.resolved_at, g.resolved_by, g.notified_at
		FROM alert_groups g JOIN projects p ON p.id = g.project_id
		WHERE p.client_id = ? AND g.first_seen >= ? AND g.first_seen < ?
		ORDER BY g.first_seen, g.id`, s.ClientID, s.From, s.To)
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()
	var tta, ttr, ttn []float64
	byLevel := map[string]int{}
	byProject := map[string]*ProjectCount{}
	for rows.Next() {
		var a Alert
		var acked, resolved, notified sql.NullString
		if err := rows.Scan(&a.ID, &a.Project, &a.Title, &a.Level, &a.Source, &a.FirstSeen, &a.LastSeen, &a.Count,
			&acked, &a.AcknowledgedBy, &resolved, &a.ResolvedBy, &notified); err != nil {
			return Summary{}, err
		}
		first, _ := ids.Parse(a.FirstSeen)
		since := func(n sql.NullString) *float64 {
			if !n.Valid {
				return nil
			}
			t, err := ids.Parse(n.String)
			if err != nil {
				return nil
			}
			d := math.Max(0, t.Sub(first).Seconds())
			return &d
		}
		if acked.Valid {
			a.AcknowledgedAt = &acked.String
			a.ToAcknowledge = since(acked)
			tta = append(tta, *a.ToAcknowledge)
			s.Totals.Acknowledged++
		}
		if resolved.Valid {
			a.ResolvedAt = &resolved.String
			a.ToResolve = since(resolved)
			ttr = append(ttr, *a.ToResolve)
		}
		if notified.Valid {
			a.ToNotify = since(notified)
			ttn = append(ttn, *a.ToNotify)
		}
		switch {
		case a.ResolvedAt == nil:
			a.Status = "open"
			s.Totals.StillOpen++
		case a.ResolvedBy == "recovery":
			a.Status = "recovered"
			s.Totals.Recovered++
			s.Totals.Resolved++
		default:
			a.Status = "resolved"
			s.Totals.ResolvedByHand++
			s.Totals.Resolved++
		}
		s.Totals.Alerts++
		s.Totals.Occurrences += a.Count
		byLevel[a.Level]++
		pc := byProject[a.Project]
		if pc == nil {
			pc = &ProjectCount{Project: a.Project}
			byProject[a.Project] = pc
		}
		pc.Alerts++
		if a.Status == "open" {
			pc.Open++
		}
		s.Alerts = append(s.Alerts, a)
		if a.Status == "recovered" {
			s.Recoveries = append(s.Recoveries, a)
		}
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}
	if s.Alerts == nil {
		s.Alerts, s.Recoveries = []Alert{}, []Alert{}
	}
	if s.Recoveries == nil {
		s.Recoveries = []Alert{}
	}
	for i := len(levels.All) - 1; i >= 0; i-- {
		l := levels.All[i]
		if levels.Problem(l) {
			s.ByLevel = append(s.ByLevel, LevelCount{Level: l, Count: byLevel[l]})
		}
	}
	for _, pc := range byProject {
		s.Projects = append(s.Projects, *pc)
	}
	sort.Slice(s.Projects, func(i, j int) bool {
		if s.Projects[i].Alerts != s.Projects[j].Alerts {
			return s.Projects[i].Alerts > s.Projects[j].Alerts
		}
		return s.Projects[i].Project < s.Projects[j].Project
	})
	if s.Projects == nil {
		s.Projects = []ProjectCount{}
	}
	s.Acknowledge, s.Resolve, s.Notify = durations(tta), durations(ttr), durations(ttn)

	// Alerts opened before this month and still open when it began.
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_groups g JOIN projects p ON p.id = g.project_id
		WHERE p.client_id = ? AND g.first_seen < ? AND (g.resolved_at IS NULL OR g.resolved_at >= ?)`, s.ClientID, s.From, s.From).Scan(&s.CarriedOver); err != nil {
		return Summary{}, err
	}

	// Every event received this month, by level, and pushes delivered.
	evRows, err := db.QueryContext(ctx, `SELECT e.level, COUNT(*) FROM events e JOIN projects p ON p.id = e.project_id
		WHERE p.client_id = ? AND e.created_at >= ? AND e.created_at < ? GROUP BY e.level`, s.ClientID, s.From, s.To)
	if err != nil {
		return Summary{}, err
	}
	defer evRows.Close()
	evByLevel := map[string]int{}
	for evRows.Next() {
		var l string
		var n int
		if err := evRows.Scan(&l, &n); err != nil {
			return Summary{}, err
		}
		evByLevel[l] = n
		s.Totals.Events += n
	}
	for i := len(levels.All) - 1; i >= 0; i-- {
		s.EventsByLevel = append(s.EventsByLevel, LevelCount{Level: levels.All[i], Count: evByLevel[levels.All[i]]})
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deliveries d JOIN events e ON e.id = d.event_id JOIN projects p ON p.id = e.project_id
		WHERE p.client_id = ? AND d.status = 'sent' AND d.attempted_at >= ? AND d.attempted_at < ?`, s.ClientID, s.From, s.To).Scan(&s.Totals.Pushes); err != nil {
		return Summary{}, err
	}
	return s, nil
}

func durations(xs []float64) Durations {
	if len(xs) == 0 {
		return Durations{}
	}
	sort.Float64s(xs)
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	med := xs[len(xs)/2]
	if len(xs)%2 == 0 {
		med = (xs[len(xs)/2-1] + xs[len(xs)/2]) / 2
	}
	return Durations{Count: len(xs), Median: med, Average: sum / float64(len(xs)), Fastest: xs[0], Slowest: xs[len(xs)-1]}
}
