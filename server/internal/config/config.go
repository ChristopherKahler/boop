// Package config reads Boop's configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // timezone names work on hosts without a zoneinfo database (Windows, slim containers)
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
	// Timezone sets the month boundaries and times in monthly summaries
	// (an IANA name such as America/Chicago). Defaults to UTC.
	Timezone string
	// AppName is what the installed phone app is called on the home screen.
	AppName string
	// HookLimit caps requests a minute to one webhook address, and
	// HookSenderLimit requests a minute from one sender to all of them
	// (0 turns a limit off). Over the limit the hub answers 429.
	HookLimit       int
	HookSenderLimit int

	// Optional admin login for the web UI and admin API. Both must be set to enable it.
	AdminUser     string
	AdminPassword string
	// AllowNoLogin runs without any login when no credentials exist. Only for
	// a hub that sits behind your own private proxy or VPN.
	AllowNoLogin bool

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
		Port:            8080,
		HookLimit:       120,
		HookSenderLimit: 600,
		BaseURL:         env("BOOP_BASE_URL", ""),
		DatabasePath:    env("BOOP_DATABASE_PATH", "/data/boop.db"),
		RetentionDays:   90,
		LogLevel:        env("BOOP_LOG_LEVEL", "info"),
		AppName:         strings.TrimSpace(env("BOOP_APP_NAME", "Boop")),
		Timezone:        strings.TrimSpace(env("BOOP_TIMEZONE", "UTC")),
		IntakeURL:       strings.TrimRight(strings.TrimSpace(env("BOOP_INTAKE_URL", "")), "/"),
		AdminUser:       env("BOOP_ADMIN_USER", ""),
		AdminPassword:   env("BOOP_ADMIN_PASSWORD", ""),
		AllowNoLogin:    env("BOOP_ALLOW_NO_LOGIN", "") == "true",
		MCPToken:        strings.TrimSpace(env("BOOP_MCP_TOKEN", "")),
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
	// BOOP_PORT wins; otherwise PORT, which hosts such as Railway inject.
	for _, name := range []string{"PORT", "BOOP_PORT"} {
		if v := os.Getenv(name); v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p <= 0 || p > 65535 {
				return c, fmt.Errorf("%s must be a port number, got %q", name, v)
			}
			c.Port = p
		}
	}
	for name, dst := range map[string]*int{"BOOP_HOOK_LIMIT": &c.HookLimit, "BOOP_HOOK_SENDER_LIMIT": &c.HookSenderLimit} {
		if v := os.Getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return c, fmt.Errorf("%s must be a number of requests a minute (0 = no limit), got %q", name, v)
			}
			*dst = n
		}
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
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return c, fmt.Errorf("BOOP_TIMEZONE must be a timezone name such as America/Chicago, got %q", c.Timezone)
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
