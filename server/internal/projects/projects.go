// Package projects manages projects and their API keys.
package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/database"
	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrNotFound is returned when a project does not exist.
var ErrNotFound = errors.New("project not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid project")

// Project is a group of events with its own API key. Every project belongs to
// one client.
type Project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Icon   string `json:"icon"`
	Notify bool   `json:"notify"`
	// MinLevel is the least severe level that pushes, derived from
	// EffectivePushLevels ("" when nothing pushes). Kept for older clients.
	MinLevel   string `json:"min_level"`
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	ClientSlug string `json:"client_slug"`
	// PushLevels overrides the client's routing; nil (JSON null) means "use the client's".
	PushLevels []string `json:"push_levels"`
	// EffectivePushLevels is what actually pushes: PushLevels, or the client's.
	EffectivePushLevels []string `json:"effective_push_levels"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
}

// Input is the writable subset of a project.
type Input struct {
	Name     *string `json:"name"`
	Icon     *string `json:"icon"`
	Notify   *bool   `json:"notify"`
	ClientID *string `json:"client_id"`
	// MinLevel sets PushLevels to every level at or above it.
	MinLevel *string `json:"min_level"`
	// PushLevels: absent leaves it unchanged, null means "use the client's",
	// a list (possibly empty) overrides the client.
	PushLevels json.RawMessage `json:"push_levels"`
}

// pushLevels applies MinLevel / PushLevels from in onto cur (nil = inherit).
func (in Input) pushLevels(cur []string) ([]string, error) {
	if in.MinLevel != nil {
		if !levels.Valid(*in.MinLevel) {
			return nil, fmt.Errorf("%w: min_level must be one of %s", ErrInvalid, strings.Join(levels.All, ", "))
		}
		cur = levels.From(*in.MinLevel)
	}
	if len(in.PushLevels) > 0 {
		if string(in.PushLevels) == "null" {
			return nil, nil
		}
		var set []string
		if err := json.Unmarshal(in.PushLevels, &set); err != nil {
			return nil, fmt.Errorf("%w: push_levels must be a list of levels or null", ErrInvalid)
		}
		norm, err := levels.NormaliseSet(set)
		if err != nil {
			return nil, fmt.Errorf("%w: push_levels: %v", ErrInvalid, err)
		}
		cur = norm
	}
	return cur, nil
}

// Store persists projects.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `p.id, p.name, p.slug, p.icon, p.notify, COALESCE(p.client_id, ''), COALESCE(c.name, ''), COALESCE(c.slug, ''),
	p.push_levels, COALESCE(c.push_levels, ''), p.created_at, p.updated_at`

const from = ` FROM projects p LEFT JOIN clients c ON c.id = p.client_id`

func scan(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var notify int
	var own sql.NullString
	var inherited string
	err := row.Scan(&p.ID, &p.Name, &p.Slug, &p.Icon, &notify, &p.ClientID, &p.ClientName, &p.ClientSlug, &own, &inherited, &p.CreatedAt, &p.UpdatedAt)
	p.Notify = notify == 1
	if own.Valid {
		p.PushLevels = levels.SplitSet(own.String)
		p.EffectivePushLevels = p.PushLevels
	} else {
		p.EffectivePushLevels = levels.SplitSet(inherited)
	}
	p.MinLevel = levels.Lowest(p.EffectivePushLevels)
	return p, err
}

// storedLevels is the push_levels column value for set (NULL = inherit).
func storedLevels(set []string) any {
	if set == nil {
		return nil
	}
	return levels.JoinSet(set)
}

// minLevelColumn keeps the legacy min_level column roughly in step with routing.
func minLevelColumn(set []string) string {
	if l := levels.Lowest(set); l != "" {
		return l
	}
	return levels.Info
}

// Create inserts a project and returns it along with its raw API key. The raw
// key is never stored and cannot be recovered later.
func (s *Store) Create(ctx context.Context, in Input) (Project, string, error) {
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" {
		return Project{}, "", fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(name) > 80 {
		return Project{}, "", fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
	}
	if in.ClientID == nil || strings.TrimSpace(*in.ClientID) == "" {
		return Project{}, "", fmt.Errorf("%w: client_id is required", ErrInvalid)
	}
	if in.MinLevel != nil && *in.MinLevel == "" {
		in.MinLevel = nil
	}
	push, err := in.pushLevels(nil)
	if err != nil {
		return Project{}, "", err
	}
	notify := true
	if in.Notify != nil {
		notify = *in.Notify
	}
	icon := ""
	if in.Icon != nil {
		icon = strings.TrimSpace(*in.Icon)
	}
	if !ValidIcon(icon) {
		return Project{}, "", fmt.Errorf("%w: icon must be <shape>:<color> (shapes: %s) or a short emoji", ErrInvalid, strings.Join(IconShapes, ", "))
	}
	if icon == "" {
		icon = DefaultIcon(Slugify(name))
	}
	key := auth.NewSecret(auth.PrefixProjectKey)
	now := ids.Now()
	id := ids.New("prj")

	base := Slugify(name)
	for i := 0; i < 20; i++ {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO projects (id, name, slug, icon, notify, min_level, client_id, push_levels, created_at, updated_at, api_key_hash)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, name, slug, icon, boolInt(notify), minLevelColumn(push), strings.TrimSpace(*in.ClientID), storedLevels(push), now, now, auth.Hash(key))
		if err == nil {
			p, err := s.Get(ctx, id)
			return p, key, err
		}
		if isForeignKey(err) {
			return Project{}, "", fmt.Errorf("%w: unknown client", ErrInvalid)
		}
		if !database.IsUniqueViolation(err) || !strings.Contains(err.Error(), "slug") {
			return Project{}, "", err
		}
	}
	return Project{}, "", fmt.Errorf("%w: could not find a free slug for %q", ErrInvalid, name)
}

// List returns every project, newest first.
func (s *Store) List(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+from+` ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns a project by id.
func (s *Store) Get(ctx context.Context, id string) (Project, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// Count returns the number of projects.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&n)
	return n, err
}

// First returns the oldest project, or ErrNotFound when none exist.
func (s *Store) First(ctx context.Context) (Project, error) {
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` ORDER BY p.created_at ASC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// Authenticate resolves a raw project API key to its project.
func (s *Store) Authenticate(ctx context.Context, rawKey string) (Project, error) {
	if !auth.HasPrefix(rawKey, auth.PrefixProjectKey) {
		return Project{}, ErrNotFound
	}
	p, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` WHERE p.api_key_hash = ?`, auth.Hash(rawKey)))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

// Update applies the non-nil fields of in.
func (s *Store) Update(ctx context.Context, id string, in Input) (Project, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return Project{}, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return Project{}, fmt.Errorf("%w: name must be 1-80 characters", ErrInvalid)
		}
		p.Name = n
	}
	if in.Icon != nil {
		icon := strings.TrimSpace(*in.Icon)
		if !ValidIcon(icon) {
			return Project{}, fmt.Errorf("%w: icon must be <shape>:<color> (shapes: %s) or a short emoji", ErrInvalid, strings.Join(IconShapes, ", "))
		}
		if icon == "" {
			icon = DefaultIcon(p.Slug)
		}
		p.Icon = icon
	}
	if in.Notify != nil {
		p.Notify = *in.Notify
	}
	if in.ClientID != nil {
		c := strings.TrimSpace(*in.ClientID)
		if c == "" {
			return Project{}, fmt.Errorf("%w: client_id cannot be empty", ErrInvalid)
		}
		p.ClientID = c
	}
	push, err := in.pushLevels(p.PushLevels)
	if err != nil {
		return Project{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE projects SET name=?, icon=?, notify=?, min_level=?, client_id=?, push_levels=?, updated_at=? WHERE id=?`,
		p.Name, p.Icon, boolInt(p.Notify), minLevelColumn(push), p.ClientID, storedLevels(push), ids.Now(), p.ID)
	if isForeignKey(err) {
		return Project{}, fmt.Errorf("%w: unknown client", ErrInvalid)
	}
	if err != nil {
		return Project{}, err
	}
	return s.Get(ctx, p.ID)
}

// RotateKey generates a new API key for the project and returns the raw key.
func (s *Store) RotateKey(ctx context.Context, id string) (string, error) {
	key := auth.NewSecret(auth.PrefixProjectKey)
	res, err := s.db.ExecContext(ctx, `UPDATE projects SET api_key_hash=?, updated_at=? WHERE id=?`, auth.Hash(key), ids.Now(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// Delete removes a project and (via cascade) its events.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify lowercases and dashes a name: "My Project!" -> "my-project".
func Slugify(name string) string {
	s := nonSlug.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	return s
}

func isForeignKey(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
