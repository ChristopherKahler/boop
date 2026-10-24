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

func TestHookLimits(t *testing.T) {
	t.Setenv("BOOP_HOOK_LIMIT", "")
	t.Setenv("BOOP_HOOK_SENDER_LIMIT", "")
	if c, err := Load(); err != nil || c.HookLimit != 120 || c.HookSenderLimit != 600 {
		t.Errorf("defaults: %d %d %v", c.HookLimit, c.HookSenderLimit, err)
	}
	t.Setenv("BOOP_HOOK_LIMIT", "30")
	t.Setenv("BOOP_HOOK_SENDER_LIMIT", "0")
	if c, err := Load(); err != nil || c.HookLimit != 30 || c.HookSenderLimit != 0 {
		t.Errorf("set: %d %d %v", c.HookLimit, c.HookSenderLimit, err)
	}
	for _, bad := range []string{"-1", "lots"} {
		t.Setenv("BOOP_HOOK_LIMIT", bad)
		if _, err := Load(); err == nil {
			t.Errorf("BOOP_HOOK_LIMIT=%q should be refused", bad)
		}
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
