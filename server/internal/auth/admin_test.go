package auth

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisgreg/boop/server/internal/database"
)

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Fatalf("hash = %q %v", h, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("salt is not random")
	}
	if !VerifyPassword("correct horse battery", h) || VerifyPassword("correct horse batterY", h) || VerifyPassword("x", "garbage") {
		t.Error("VerifyPassword is wrong")
	}
	if ValidatePassword("short") == nil || ValidatePassword("long enough!") != nil {
		t.Error("ValidatePassword")
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(3, 10*time.Minute)
	now := time.Unix(1_800_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if b, _ := l.Blocked("1.2.3.4"); b {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail("1.2.3.4")
	}
	if b, wait := l.Blocked("1.2.3.4"); !b || wait != 10*time.Minute {
		t.Errorf("not blocked after 3: %v %v", b, wait)
	}
	if b, _ := l.Blocked("5.6.7.8"); b {
		t.Error("another address is blocked")
	}
	now = now.Add(11 * time.Minute)
	if b, _ := l.Blocked("1.2.3.4"); b {
		t.Error("still blocked after the window")
	}
	l.Fail("1.2.3.4")
	l.Reset("1.2.3.4")
	if len(l.fails) != 0 {
		t.Error("reset kept failures")
	}
}

func TestAdminModes(t *testing.T) {
	open := NewAdmin("", "")
	if open.Enabled() || !open.Authorized(httptest.NewRequest("GET", "/", nil)) {
		t.Error("an admin without credentials is open (tests and BOOP_ALLOW_NO_LOGIN)")
	}
	open.RequireSetup("ABCD-EFGH")
	if !open.Enabled() || !open.NeedsSetup() || open.Authorized(httptest.NewRequest("GET", "/", nil)) {
		t.Error("setup mode must lock everything")
	}
	if !open.CheckSetupCode("abcd efgh"[:4]+"-EFGH") || !open.CheckSetupCode("abcdefgh") || open.CheckSetupCode("ABCD-EFGX") || open.CheckSetupCode("") {
		t.Error("setup code check")
	}
	h, _ := HashPassword("a long password")
	open.UseStoredLogin("owner", h)
	if open.NeedsSetup() || open.CheckSetupCode("ABCD-EFGH") {
		t.Error("setup code must stop working once the login exists")
	}
	if !open.Check("owner", "a long password") || open.Check("owner", "nope") || open.Check("other", "a long password") {
		t.Error("stored login check")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("owner", "a long password")
	if !open.Authorized(req) {
		t.Error("basic auth with the stored login")
	}
}

// Sessions in the database survive a new Admin (a restart).
func TestDBSessionsSurviveRestart(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "boop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := NewAdmin("owner", "a long password")
	a.UseSessions(NewDBSessions(db))
	tok, ok := a.Login("owner", "a long password")
	if !ok {
		t.Fatal("login")
	}
	b := NewAdmin("owner", "a long password")
	b.UseSessions(NewDBSessions(db))
	if !b.Valid(tok) {
		t.Fatal("session lost across restart")
	}
	b.now = func() time.Time { return time.Now().Add(SessionTTL - time.Hour) }
	if !b.Renew(tok) {
		t.Fatal("renew")
	}
	b.now = func() time.Time { return time.Now().Add(SessionTTL + time.Hour) }
	if !b.Valid(tok) {
		t.Error("a renewed session should outlive the original expiry")
	}
	b.Logout(tok)
	if b.Valid(tok) {
		t.Error("logout kept the session")
	}
	if err := NewDBSessions(db).Prune(time.Now()); err != nil {
		t.Error(err)
	}
}
