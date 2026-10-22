package config

import "testing"

func TestPortPrecedence(t *testing.T) {
	for _, c := range []struct {
		port, boopPort string
		want           int
	}{
		{"", "", 8080},
		{"9123", "", 9123},     // Railway's injected PORT
		{"9123", "4795", 4795}, // BOOP_PORT wins
	} {
		t.Setenv("PORT", c.port)
		t.Setenv("BOOP_PORT", c.boopPort)
		got, err := Load()
		if err != nil || got.Port != c.want {
			t.Errorf("PORT=%q BOOP_PORT=%q: port %d, err %v; want %d", c.port, c.boopPort, got.Port, err, c.want)
		}
	}
	t.Setenv("PORT", "eighty")
	t.Setenv("BOOP_PORT", "")
	if _, err := Load(); err == nil {
		t.Error("a bad PORT should be refused")
	}
}

func TestTimezoneAndVAPIDValidation(t *testing.T) {
	t.Setenv("BOOP_TIMEZONE", "America/Chicago")
	if c, err := Load(); err != nil || c.Timezone != "America/Chicago" {
		t.Errorf("timezone: %+v %v", c.Timezone, err)
	}
	t.Setenv("BOOP_TIMEZONE", "Mars/Olympus")
	if _, err := Load(); err == nil {
		t.Error("an unknown timezone should be refused")
	}
	t.Setenv("BOOP_TIMEZONE", "")
	t.Setenv("BOOP_VAPID_PUBLIC_KEY", "only-half")
	if _, err := Load(); err == nil {
		t.Error("half a VAPID pair should be refused")
	}
	t.Setenv("BOOP_VAPID_PUBLIC_KEY", "")
	t.Setenv("BOOP_VAPID_SUBJECT", "ops@example.com")
	if _, err := Load(); err == nil {
		t.Error("a VAPID subject without mailto: or https:// should be refused")
	}
}
