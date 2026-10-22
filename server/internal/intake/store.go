package intake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/ids"
)

// ErrNotFound is returned for unknown intakes and tokens.
var ErrNotFound = errors.New("intake not found")

// ErrInvalid wraps validation failures.
var ErrInvalid = errors.New("invalid intake")

// PrefixToken starts every intake token.
const PrefixToken = "boop_hook"

// Intake is one webhook address for one service and project.
type Intake struct {
	ID             string          `json:"id"`
	ProjectID      string          `json:"project_id"`
	ProjectName    string          `json:"project_name"`
	ClientName     string          `json:"client_name"`
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	TokenHint      string          `json:"token_hint"`
	SigningSecret  bool            `json:"signing_secret_set"`
	Mapping        json.RawMessage `json:"mapping"`
	ReceivedCount  int             `json:"received_count"`
	LastReceivedAt *string         `json:"last_received_at"`
	LastResult     string          `json:"last_result"`
	CreatedAt      string          `json:"created_at"`

	secret string
}

// Secret is the stored signing secret ("" when none).
func (i Intake) Secret() string { return i.secret }

// Input creates or updates an intake.
type Input struct {
	Kind          string          `json:"kind"`
	Name          *string         `json:"name"`
	SigningSecret *string         `json:"signing_secret"`
	Mapping       json.RawMessage `json:"mapping"`
}

// Store persists intakes.
type Store struct {
	db *sql.DB
}

// New returns a Store.
func New(db *sql.DB) *Store { return &Store{db: db} }

const cols = `i.id, i.project_id, p.name, COALESCE(c.name, ''), i.kind, i.name, i.token_hint, i.signing_secret, i.mapping_json,
	i.received_count, i.last_received_at, i.last_result, i.created_at`

const from = ` FROM intakes i JOIN projects p ON p.id = i.project_id LEFT JOIN clients c ON c.id = p.client_id`

func scan(row interface{ Scan(...any) error }) (Intake, error) {
	var in Intake
	var mapping string
	var last sql.NullString
	err := row.Scan(&in.ID, &in.ProjectID, &in.ProjectName, &in.ClientName, &in.Kind, &in.Name, &in.TokenHint, &in.secret, &mapping,
		&in.ReceivedCount, &last, &in.LastResult, &in.CreatedAt)
	in.Mapping = json.RawMessage(mapping)
	in.SigningSecret = in.secret != ""
	if last.Valid {
		in.LastReceivedAt = &last.String
	}
	return in, err
}

func validate(kind string, in Input) (name, secret, mapping string, err error) {
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if len(name) > 80 {
			return "", "", "", fmt.Errorf("%w: name must be 80 characters or fewer", ErrInvalid)
		}
	}
	if in.SigningSecret != nil {
		secret = strings.TrimSpace(*in.SigningSecret)
		if len(secret) > 512 {
			return "", "", "", fmt.Errorf("%w: signing_secret is too long", ErrInvalid)
		}
	}
	mapping = "{}"
	if len(in.Mapping) > 0 && string(in.Mapping) != "null" {
		if kind != KindGeneric {
			return "", "", "", fmt.Errorf("%w: only generic intakes take a mapping", ErrInvalid)
		}
		var m Mapping
		if err := json.Unmarshal(in.Mapping, &m); err != nil {
			return "", "", "", fmt.Errorf("%w: mapping: %v", ErrInvalid, err)
		}
		if err := m.Validate(); err != nil {
			return "", "", "", fmt.Errorf("%w: mapping: %v", ErrInvalid, err)
		}
		mapping = string(in.Mapping)
	}
	return name, secret, mapping, nil
}

// Create adds an intake to a project and returns it with its raw token
// (shown once; only a hash is kept).
func (s *Store) Create(ctx context.Context, projectID string, in Input) (Intake, string, error) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if !ValidKind(kind) {
		return Intake{}, "", fmt.Errorf("%w: kind must be one of %s", ErrInvalid, strings.Join(Kinds, ", "))
	}
	name, secret, mapping, err := validate(kind, in)
	if err != nil {
		return Intake{}, "", err
	}
	if kind == KindGeneric && mapping == "{}" {
		return Intake{}, "", fmt.Errorf("%w: a generic intake needs a mapping (at least a title)", ErrInvalid)
	}
	if name == "" {
		name = KindLabel[kind]
	}
	token := auth.NewSecret(PrefixToken)
	now := ids.Now()
	id := ids.New("ink")
	_, err = s.db.ExecContext(ctx, `INSERT INTO intakes (id, project_id, kind, name, token_hash, token_hint, signing_secret, mapping_json, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, id, projectID, kind, name, auth.Hash(token), token[len(token)-4:], secret, mapping, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			return Intake{}, "", fmt.Errorf("%w: unknown project", ErrInvalid)
		}
		return Intake{}, "", err
	}
	got, err := s.Get(ctx, id)
	return got, token, err
}

// Get returns one intake.
func (s *Store) Get(ctx context.Context, id string) (Intake, error) {
	in, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Intake{}, ErrNotFound
	}
	return in, err
}

// List returns intakes, optionally for one project, oldest first.
func (s *Store) List(ctx context.Context, projectID string) ([]Intake, error) {
	q := `SELECT ` + cols + from
	var args []any
	if projectID != "" {
		q += ` WHERE i.project_id = ?`
		args = append(args, projectID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY i.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Intake{}
	for rows.Next() {
		in, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// Authenticate resolves a raw token for the given kind.
func (s *Store) Authenticate(ctx context.Context, kind, token string) (Intake, error) {
	if !auth.HasPrefix(token, PrefixToken) {
		return Intake{}, ErrNotFound
	}
	in, err := scan(s.db.QueryRowContext(ctx, `SELECT `+cols+from+` WHERE i.token_hash = ? AND i.kind = ?`, auth.Hash(token), kind))
	if errors.Is(err, sql.ErrNoRows) {
		return Intake{}, ErrNotFound
	}
	return in, err
}

// Update changes an intake's name, signing secret or mapping.
func (s *Store) Update(ctx context.Context, id string, in Input) (Intake, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return Intake{}, err
	}
	name, secret, mapping, err := validate(cur.Kind, in)
	if err != nil {
		return Intake{}, err
	}
	if in.Name == nil {
		name = cur.Name
	}
	if in.SigningSecret == nil {
		secret = cur.secret
	}
	if len(in.Mapping) == 0 {
		mapping = string(cur.Mapping)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE intakes SET name=?, signing_secret=?, mapping_json=?, updated_at=? WHERE id=?`, name, secret, mapping, ids.Now(), id); err != nil {
		return Intake{}, err
	}
	return s.Get(ctx, id)
}

// RotateToken issues a new token; the old address stops working at once.
func (s *Store) RotateToken(ctx context.Context, id string) (string, error) {
	token := auth.NewSecret(PrefixToken)
	res, err := s.db.ExecContext(ctx, `UPDATE intakes SET token_hash=?, token_hint=?, updated_at=? WHERE id=?`, auth.Hash(token), token[len(token)-4:], ids.Now(), id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return token, nil
}

// Delete removes an intake.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM intakes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Received records a delivery and its outcome ("2 events", "ignored: ...").
func (s *Store) Received(ctx context.Context, id, result string) error {
	if len(result) > 300 {
		result = result[:300]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE intakes SET received_count = received_count + 1, last_received_at = ?, last_result = ? WHERE id = ?`, ids.Now(), result, id)
	return err
}
