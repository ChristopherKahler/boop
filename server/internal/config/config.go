// Package config reads Boop's configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is the full server configuration.
type Config struct {
	Port          int
	BaseURL       string
	DatabasePath  string
	RetentionDays int
	// RetentionDaysSet is true when BOOP_RETENTION_DAYS was given explicitly; the
	// value then overrides whatever was saved from the web UI on every start.
	RetentionDaysSet bool
	LogLevel         string
	// IntakeURL is the public, internet-reachable base address that monitoring
	// services post webhooks to (/hooks/...). Defaults to BaseURL.
	IntakeURL string
	// AppName is what the installed phone app is called on the home screen.
	AppName string

	// Optional admin login for the web UI and admin API. Both must be set to enable it.
	AdminUser     string
	AdminPassword string

	// MCPToken, when set, is a bearer token that grants read-only access to the
	// MCP endpoint (/mcp). Device credentials and admin logins work there too.
	MCPToken string

	APNS APNS

	// WebPush is the VAPID identity for pushes to the installed web app. When
	// the keys are empty the server generates a pair on first start and keeps
	// it in its database, so push works with no setup.
	WebPush WebPush
}

// WebPush holds VAPID settings (RFC 8292).
type WebPush struct {
	PublicKey  string
	PrivateKey string
	// Subject is the contact push services see: mailto:you@example.com or an
	// https URL. Defaults to BOOP_BASE_URL.
	Subject string
}

// APNS holds Apple Push Notification service credentials.
type APNS struct {
	TeamID         string
	KeyID          string
	BundleID       string
	PrivateKeyPath string
	PrivateKey     string // PEM contents; APNS_PRIVATE_KEY, used only when no path is set.
	Environment    string // "production" or "sandbox"
}

// Configured reports whether every value required for APNs delivery is present.
func (a APNS) Configured() bool {
	return a.TeamID != "" && a.KeyID != "" && a.BundleID != "" && (a.PrivateKeyPath != "" || a.PrivateKey != "")
}

// Missing lists the APNs settings that are still empty.
func (a APNS) Missing() []string {
	var m []string
	if a.TeamID == "" {
		m = append(m, "APNS_TEAM_ID")
	}
	if a.KeyID == "" {
		m = append(m, "APNS_KEY_ID")
	}
	if a.BundleID == "" {
		m = append(m, "APNS_BUNDLE_ID")
	}
	if a.PrivateKeyPath == "" && a.PrivateKey == "" {
		m = append(m, "APNS_PRIVATE_KEY_PATH")
	}
	return m
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	c := Config{
		Port:          8080,
		BaseURL:       env("BOOP_BASE_URL", ""),
		DatabasePath:  env("BOOP_DATABASE_PATH", "/data/boop.db"),
		RetentionDays: 90,
		LogLevel:      env("BOOP_LOG_LEVEL", "info"),
		AppName:       strings.TrimSpace(env("BOOP_APP_NAME", "Boop")),
		IntakeURL:     strings.TrimRight(strings.TrimSpace(env("BOOP_INTAKE_URL", "")), "/"),
		AdminUser:     env("BOOP_ADMIN_USER", ""),
		AdminPassword: env("BOOP_ADMIN_PASSWORD", ""),
		MCPToken:      strings.TrimSpace(env("BOOP_MCP_TOKEN", "")),
		APNS: APNS{
			TeamID:         env("APNS_TEAM_ID", ""),
			KeyID:          env("APNS_KEY_ID", ""),
			BundleID:       env("APNS_BUNDLE_ID", ""),
			PrivateKeyPath: env("APNS_PRIVATE_KEY_PATH", ""),
			PrivateKey:     env("APNS_PRIVATE_KEY", ""),
			Environment:    env("APNS_ENVIRONMENT", "production"),
		},
		WebPush: WebPush{
			PublicKey:  strings.TrimSpace(env("BOOP_VAPID_PUBLIC_KEY", "")),
			PrivateKey: strings.TrimSpace(env("BOOP_VAPID_PRIVATE_KEY", "")),
			Subject:    strings.TrimSpace(env("BOOP_VAPID_SUBJECT", "")),
		},
	}
	if v := os.Getenv("BOOP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, fmt.Errorf("BOOP_PORT must be a port number, got %q", v)
		}
		c.Port = p
	}
	if v := os.Getenv("BOOP_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("BOOP_RETENTION_DAYS must be a non-negative integer, got %q", v)
		}
		c.RetentionDays = d
		c.RetentionDaysSet = true
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if (c.AdminUser == "") != (c.AdminPassword == "") {
		return c, fmt.Errorf("BOOP_ADMIN_USER and BOOP_ADMIN_PASSWORD must be set together")
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 8 {
		return c, fmt.Errorf("BOOP_ADMIN_PASSWORD must be at least 8 characters")
	}
	if c.MCPToken != "" && len(c.MCPToken) < 16 {
		return c, fmt.Errorf("BOOP_MCP_TOKEN must be at least 16 characters")
	}
	if (c.WebPush.PublicKey == "") != (c.WebPush.PrivateKey == "") {
		return c, fmt.Errorf("BOOP_VAPID_PUBLIC_KEY and BOOP_VAPID_PRIVATE_KEY must be set together (generate a pair with: boop vapid)")
	}
	if sub := c.WebPush.Subject; sub != "" && !strings.HasPrefix(sub, "mailto:") && !strings.HasPrefix(sub, "https://") {
		return c, fmt.Errorf("BOOP_VAPID_SUBJECT must be a mailto: address or an https:// URL, got %q", sub)
	}
	if c.APNS.Environment != "production" && c.APNS.Environment != "sandbox" {
		return c, fmt.Errorf("APNS_ENVIRONMENT must be production or sandbox, got %q", c.APNS.Environment)
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
