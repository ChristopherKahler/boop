package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SessionCookie is the name of the admin session cookie.
const SessionCookie = "boop_session"

// SessionTTL is how long an admin login lasts without use. Each visit renews it.
const SessionTTL = 30 * 24 * time.Hour

// SessionStore keeps login sessions (by token hash) so they survive a restart.
type SessionStore interface {
	Put(hash string, expires time.Time, userAgent string) error
	Expiry(hash string) (time.Time, bool)
	Delete(hash string) error
}

type mode int

const (
	// modeOpen: no login (tests, or BOOP_ALLOW_NO_LOGIN behind a private proxy).
	modeOpen mode = iota
	// modeSetup: no credentials yet; everything is locked until someone with
	// the setup code (printed in the server log) creates the login.
	modeSetup
	// modeLogin: a username and password protect the web UI and admin API.
	modeLogin
)

// Admin guards the web UI and admin endpoints with one username and password.
type Admin struct {
	mu       sync.Mutex
	mode     mode
	username string
	check    func(password string) bool
	setup    string // the one-time setup code, in modeSetup
	sessions SessionStore
	limiter  *Limiter
	now      func() time.Time
}

// NewAdmin returns an Admin protected by username/password (from the
// environment). With both empty it is open; call RequireSetup to lock it
// until a login is created instead.
func NewAdmin(username, password string) *Admin {
	a := &Admin{sessions: newMemorySessions(), limiter: NewLimiter(5, 15*time.Minute), now: time.Now}
	if username != "" && password != "" {
		hashed := Hash(password)
		a.mode, a.username = modeLogin, username
		a.check = func(p string) bool { return subtle.ConstantTimeCompare([]byte(Hash(p)), []byte(hashed)) == 1 }
	}
	return a
}

// UseSessions stores sessions in s (the database) instead of memory.
func (a *Admin) UseSessions(s SessionStore) { a.sessions = s }

// UseStoredLogin protects the hub with a login created in the setup screen
// (password stored as a PBKDF2 hash).
func (a *Admin) UseStoredLogin(username, encodedHash string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode, a.username, a.setup = modeLogin, username, ""
	a.check = func(p string) bool { return VerifyPassword(p, encodedHash) }
}

// RequireSetup locks the hub until the login is created with code.
func (a *Admin) RequireSetup(code string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode, a.setup = modeSetup, code
}

// Enabled reports whether a login is required (or about to be).
func (a *Admin) Enabled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode != modeOpen
}

// NeedsSetup reports whether no login exists yet.
func (a *Admin) NeedsSetup() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode == modeSetup
}

// CheckSetupCode compares a setup code in constant time.
func (a *Admin) CheckSetupCode(code string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	norm := func(s string) []byte {
		return hashBytes(strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "")))
	}
	return a.mode == modeSetup && a.setup != "" && subtle.ConstantTimeCompare(norm(code), norm(a.setup)) == 1
}

// Check verifies a username/password pair in constant time.
func (a *Admin) Check(username, password string) bool {
	a.mu.Lock()
	m, user, check := a.mode, a.username, a.check
	a.mu.Unlock()
	if m != modeLogin {
		return false
	}
	u := subtle.ConstantTimeCompare(hashBytes(username), hashBytes(user)) == 1
	p := check(password)
	return u && p
}

// Login creates a session and returns its raw token.
func (a *Admin) Login(username, password string) (string, bool) {
	if !a.Check(username, password) {
		return "", false
	}
	return a.NewSession(""), true
}

// NewSession issues a session token without a password check (used right
// after the login is created).
func (a *Admin) NewSession(userAgent string) string {
	tok := NewSecret("boop_sess")
	_ = a.sessions.Put(Hash(tok), a.now().Add(SessionTTL), userAgent)
	return tok
}

// Logout revokes a session token.
func (a *Admin) Logout(token string) { _ = a.sessions.Delete(Hash(token)) }

// Valid reports whether a raw session token is live.
func (a *Admin) Valid(token string) bool {
	if token == "" {
		return false
	}
	exp, ok := a.sessions.Expiry(Hash(token))
	if !ok {
		return false
	}
	if a.now().After(exp) {
		_ = a.sessions.Delete(Hash(token))
		return false
	}
	return true
}

// Renew pushes a live session's expiry out to a full SessionTTL from now.
func (a *Admin) Renew(token string) bool {
	if !a.Valid(token) {
		return false
	}
	return a.sessions.Put(Hash(token), a.now().Add(SessionTTL), "") == nil
}

// Authorized reports whether r carries a valid session cookie or HTTP Basic
// credentials. Always true when open; never true while setup is pending.
func (a *Admin) Authorized(r *http.Request) bool {
	a.mu.Lock()
	m := a.mode
	a.mu.Unlock()
	switch m {
	case modeOpen:
		return true
	case modeSetup:
		return false
	}
	if c, err := r.Cookie(SessionCookie); err == nil && a.Valid(c.Value) {
		return true
	}
	if u, p, ok := r.BasicAuth(); ok && a.Check(u, p) {
		return true
	}
	return false
}

// Limiter is the login brute-force guard.
func (a *Admin) Limiter() *Limiter { return a.limiter }

// SetCookie writes the session cookie for token onto w.
func (a *Admin) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: isHTTPS(r), MaxAge: int(SessionTTL.Seconds()),
	})
}

// ClearCookie removes the session cookie.
func (a *Admin) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: -1})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func hashBytes(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// ClientIP is the address a request came from, preferring the first
// X-Forwarded-For hop (hosts such as Railway and tailscale serve proxy it).
func ClientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		if ip := strings.TrimSpace(strings.Split(f, ",")[0]); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// memorySessions keeps sessions in memory (tests, or before a store is set).
type memorySessions struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newMemorySessions() *memorySessions { return &memorySessions{m: map[string]time.Time{}} }

func (s *memorySessions) Put(hash string, exp time.Time, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[hash] = exp
	return nil
}

func (s *memorySessions) Expiry(hash string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[hash]
	return exp, ok
}

func (s *memorySessions) Delete(hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, hash)
	return nil
}
