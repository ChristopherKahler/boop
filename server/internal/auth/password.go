package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chrisgreg/boop/server/internal/ids"
)

// pbkdf2Iterations follows OWASP's current guidance for PBKDF2-HMAC-SHA256.
const pbkdf2Iterations = 600_000

var b64 = base64.RawStdEncoding

// HashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<hash>".
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a HashPassword result.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false
	}
	salt, err1 := b64.DecodeString(parts[2])
	want, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword enforces the minimum for a new login.
func ValidatePassword(p string) error {
	if len(p) < 10 {
		return errors.New("the password must be at least 10 characters")
	}
	return nil
}

// NewSetupCode returns a short code like "K7Q2-M9XD" for creating the first login.
func NewSetupCode() string {
	r := strings.ToUpper(ids.Random(5))[:8]
	return r[:4] + "-" + r[4:]
}

// DBSessions keeps login sessions in the sessions table.
type DBSessions struct{ db *sql.DB }

// NewDBSessions returns a SessionStore on db.
func NewDBSessions(db *sql.DB) *DBSessions { return &DBSessions{db: db} }

// Put inserts or renews a session.
func (s *DBSessions) Put(hash string, exp time.Time, userAgent string) error {
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, expires_at, created_at, user_agent) VALUES (?,?,?,?)
		ON CONFLICT(token_hash) DO UPDATE SET expires_at = excluded.expires_at`, hash, ids.Format(exp), ids.Now(), userAgent)
	return err
}

// Expiry returns a session's expiry.
func (s *DBSessions) Expiry(hash string) (time.Time, bool) {
	var v string
	if err := s.db.QueryRow(`SELECT expires_at FROM sessions WHERE token_hash = ?`, hash).Scan(&v); err != nil {
		return time.Time{}, false
	}
	t, err := ids.Parse(v)
	return t, err == nil
}

// Delete removes a session.
func (s *DBSessions) Delete(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

// Prune removes expired sessions.
func (s *DBSessions) Prune(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, ids.Format(now))
	return err
}

// Limiter counts failed logins per address and blocks after max failures
// inside window, until the window has passed since the last failure.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
	now    func() time.Time
}

// NewLimiter returns a Limiter.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, fails: map[string][]time.Time{}, now: time.Now}
}

func (l *Limiter) recent(key string) []time.Time {
	cut := l.now().Add(-l.window)
	var keep []time.Time
	for _, t := range l.fails[key] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, key)
	} else {
		l.fails[key] = keep
	}
	return keep
}

// Blocked reports whether key must wait, and for how long.
func (l *Limiter) Blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.recent(key)
	if len(r) < l.max {
		return false, 0
	}
	return true, r[len(r)-1].Add(l.window).Sub(l.now())
}

// Fail records a failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key), l.now())
}

// Reset clears key after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
