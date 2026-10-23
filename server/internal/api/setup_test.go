package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/chrisgreg/boop/server/internal/auth"
)

// browser is an HTTP client with a cookie jar, like the web app.
func (e *env) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (e *env) with(c *http.Client, method, path string, body any) resp {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func TestFirstLoginWithSetupCode(t *testing.T) {
	e := newEnv(t)
	e.server.Admin.UseSessions(auth.NewDBSessions(e.server.DB))
	e.server.Admin.RequireSetup("K7Q2-M9XD")
	b := e.browser()

	if r := e.with(b, "GET", "/api/v1/auth/me", nil); r.body["needs_setup"] != true || r.body["authenticated"] != false {
		t.Fatalf("me before setup: %s", r.raw)
	}
	// Everything is locked, including what upstream left open without a login.
	for _, p := range []string{"/api/v1/alerts", "/api/v1/projects", "/api/v1/events", "/api/v1/status"} {
		if r := e.with(b, "GET", p, nil); r.status != 401 {
			t.Errorf("%s during setup: %d", p, r.status)
		}
	}
	if r := e.with(b, "POST", "/api/v1/auth/login", map[string]string{"username": "x", "password": "y"}); r.status != 409 {
		t.Errorf("login before setup: %d", r.status)
	}
	if r := e.with(b, "POST", "/api/v1/auth/setup", map[string]string{"code": "WRONG-CODE", "username": "chris", "password": "long enough pw"}); r.status != 401 {
		t.Errorf("wrong setup code: %d", r.status)
	}
	if r := e.with(b, "POST", "/api/v1/auth/setup", map[string]string{"code": "k7q2m9xd", "username": "chris", "password": "short"}); r.status != 422 {
		t.Errorf("short password: %d", r.status)
	}
	r := e.with(b, "POST", "/api/v1/auth/setup", map[string]string{"code": "k7q2-m9xd", "username": "chris", "password": "long enough pw"})
	if r.status != 200 || r.body["authenticated"] != true {
		t.Fatalf("setup: %d %s", r.status, r.raw)
	}
	// The browser is signed in by the setup itself.
	if r := e.with(b, "GET", "/api/v1/alerts", nil); r.status != 200 {
		t.Errorf("after setup: %d", r.status)
	}
	if r := e.with(b, "POST", "/api/v1/auth/setup", map[string]string{"code": "K7Q2-M9XD", "username": "evil", "password": "long enough pw"}); r.status != 409 {
		t.Errorf("second setup: %d", r.status)
	}
	// The stored login works for a fresh browser, and is persisted for restarts.
	fresh := e.browser()
	if r := e.with(fresh, "POST", "/api/v1/auth/login", map[string]string{"username": "chris", "password": "long enough pw"}); r.status != 200 {
		t.Errorf("login with stored credentials: %d %s", r.status, r.raw)
	}
	if u, _ := e.server.Settings.Get(t.Context(), "admin_user", ""); u != "chris" {
		t.Errorf("stored user = %q", u)
	}
	if h, _ := e.server.Settings.Get(t.Context(), "admin_password_hash", ""); !auth.VerifyPassword("long enough pw", h) {
		t.Error("stored hash does not verify")
	}
	if got := e.with(b, "GET", "/api/v1/settings", nil); bytes.Contains(got.raw, []byte("pbkdf2")) {
		t.Error("settings API leaks the password hash")
	}
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	e.server.Admin = auth.NewAdmin("chris", "the right password")
	b := e.browser()
	for i := 0; i < 5; i++ {
		if r := e.with(b, "POST", "/api/v1/auth/login", map[string]string{"username": "chris", "password": "guess"}); r.status != 401 {
			t.Fatalf("attempt %d: %d", i, r.status)
		}
	}
	r := e.with(b, "POST", "/api/v1/auth/login", map[string]string{"username": "chris", "password": "the right password"})
	if r.status != 429 {
		t.Fatalf("6th attempt (even correct) should be locked out: %d %s", r.status, r.raw)
	}
}
