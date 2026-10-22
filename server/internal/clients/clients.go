// Package clients manages clients: the level above projects. Every project
// belongs to one client, and a client carries the default routing (which
// levels push to the phone) for its projects.
package clients

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/database"
	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/ids"
	"github.com/chrisgreg/boop/server/internal/projects"
)

// ErrNotFound is returned when a client does not exist.
var ErrNotFound = errors.New("client not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid client")

// ErrInUse is returned when deleting a client that still has projects.
var ErrInUse = errors.New("client still has projects")

// DefaultPushLevels is the routing a new client starts with: problems push,
// info and success only land in the inbox.
var DefaultPushLevels = []string{levels.Warning, levels.Error, levels.Critical}

// Client is one customer (or "Internal" for your own systems).
type Client struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Slug       string   `json:"slug"`
	PushLevels []string `json:"push_levels"`
	// PushRecoveries sends a "resolved" push when an alert that pushed recovers.
	PushRecoveries bool   `json:"push_recoveries"`
	ProjectCount   int    `json:"project_count"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// Input is the writable subset of a client.
type Input struct {
	Name           *string   `json:"name"`
	PushLevels     *[]string `json:"push_levels"`
	PushRecoveries *bool     `json:"push_recoveries"`
}

// Store persists clients.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `c.id, c.name, c.slug, c.push_levels, c.push_recoveries, c.created_at, c.updated_at,
	(SELECT COUNT(*) FROM projects p WHERE p.client_id = c.id)`

func scan(row interface{ Scan(...any) error }) (Client, error) {
	var c Client
	var push string
	var rec int
	err := row.Scan(&c.ID, &c.Name, &c.Slug, &push, &rec, &c.CreatedAt, &c.UpdatedAt, &c.ProjectCount)
	c.PushLevels = levels.SplitSet(push)
	c.PushRecoveries = rec == 1
	return c, err
}

func validName(in *string) (string, error) {
	name := ""
	if in != nil {
		name = strings.TrimSpace(*in)
	}
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(name) > 80 {
		return "", fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
	}
	return name, nil
}

// Create inserts a client.
func (s *Store) Create(ctx context.Context, in Input) (Client, error) {
	name, err := validName(in.Name)
	if err != nil {
		return Client{}, err
	}
	push := DefaultPushLevels
	if in.PushLevels != nil {
		if push, err = levels.NormaliseSet(*in.PushLevels); err != nil {
			return Client{}, fmt.Errorf("%w: push_levels: %v", ErrInvalid, err)
		}
	}
	rec := true
	if in.PushRecoveries != nil {
		rec = *in.PushRecoveries
	}
	now := ids.Now()
	id := ids.New("cli")
	base := projects.Slugify(name)
	for i := 0; i < 20; i++ {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO clients (id, name, slug, push_levels, push_recoveries, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			id, name, slug, levels.JoinSet(push), boolInt(rec), now, now)
		if err == nil {
			return s.Get(ctx, id)
		}
		if !database.IsUniqueViolation(err) || !strings.Contains(err.Error(), "slug") {
			return Client{}, err
		}
	}
	return Client{}, fmt.Errorf("%w: could not find a free slug for %q", ErrInvalid, name)
}

// Get returns a client by id or slug.
func (s *Store) Get(ctx context.Context, idOrSlug string) (Client, error) {
	c, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM clients c WHERE c.id = ? OR c.slug = ?`, idOrSlug, idOrSlug))
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// List returns every client, alphabetically.
func (s *Store) List(ctx context.Context) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+` FROM clients c ORDER BY lower(c.name), c.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Client{}
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Default returns the oldest client, which projects created without a
// client_id join; ErrNotFound when there are no clients.
func (s *Store) Default(ctx context.Context) (Client, error) {
	c, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+` FROM clients c ORDER BY c.created_at, c.id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, id string, in Input) (Client, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return Client{}, err
	}
	if in.Name != nil {
		if c.Name, err = validName(in.Name); err != nil {
			return Client{}, err
		}
	}
	if in.PushLevels != nil {
		if c.PushLevels, err = levels.NormaliseSet(*in.PushLevels); err != nil {
			return Client{}, fmt.Errorf("%w: push_levels: %v", ErrInvalid, err)
		}
	}
	if in.PushRecoveries != nil {
		c.PushRecoveries = *in.PushRecoveries
	}
	_, err = s.db.ExecContext(ctx, `UPDATE clients SET name=?, push_levels=?, push_recoveries=?, updated_at=? WHERE id=?`,
		c.Name, levels.JoinSet(c.PushLevels), boolInt(c.PushRecoveries), ids.Now(), c.ID)
	if err != nil {
		return Client{}, err
	}
	return s.Get(ctx, c.ID)
}

// Delete removes a client that has no projects.
func (s *Store) Delete(ctx context.Context, id string) error {
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.ProjectCount > 0 {
		return fmt.Errorf("%w: move or delete its %d project(s) first", ErrInUse, c.ProjectCount)
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, c.ID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
