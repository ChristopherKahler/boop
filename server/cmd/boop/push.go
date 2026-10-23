package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/config"
	"github.com/chrisgreg/boop/server/internal/ids"
	"github.com/chrisgreg/boop/server/internal/settings"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

// webPushKeys returns the VAPID key pair: from the environment when set,
// otherwise the pair stored in the database, generating it on first start.
// The stored pair travels with the database, so subscriptions survive a move
// to another host as long as the database file moves too.
func webPushKeys(ctx context.Context, cfg config.Config, st *settings.Store) (*webpush.Keys, string, error) {
	if cfg.WebPush.PrivateKey != "" {
		k, err := webpush.ParseKeys(cfg.WebPush.PublicKey, cfg.WebPush.PrivateKey)
		return k, "environment", err
	}
	priv, err := st.Get(ctx, settings.KeyVAPIDPrivate, "")
	if err != nil {
		return nil, "", err
	}
	if priv == "" {
		pub, p, err := webpush.GenerateKeys()
		if err != nil {
			return nil, "", err
		}
		if err := st.Set(ctx, settings.KeyVAPIDPublic, pub); err != nil {
			return nil, "", err
		}
		if err := st.Set(ctx, settings.KeyVAPIDPrivate, p); err != nil {
			return nil, "", err
		}
		k, err := webpush.ParseKeys(pub, p)
		return k, "generated", err
	}
	pub, err := st.Get(ctx, settings.KeyVAPIDPublic, "")
	if err != nil {
		return nil, "", err
	}
	k, err := webpush.ParseKeys(pub, priv)
	return k, "database", err
}

// webPushSubject is the VAPID contact: BOOP_VAPID_SUBJECT, else the public
// https address, else a placeholder the push services still accept.
func webPushSubject(cfg config.Config) string {
	switch {
	case cfg.WebPush.Subject != "":
		return cfg.WebPush.Subject
	case strings.HasPrefix(cfg.BaseURL, "https://"):
		return cfg.BaseURL
	default:
		return "mailto:boop@localhost"
	}
}

// actionSecret returns the key that signs Acknowledge tokens, generating and
// storing it on first start.
func actionSecret(ctx context.Context, st *settings.Store) ([]byte, error) {
	v, err := st.Get(ctx, settings.KeyActionSecret, "")
	if err != nil {
		return nil, err
	}
	if v == "" {
		v = ids.Random(32)
		if err := st.Set(ctx, settings.KeyActionSecret, v); err != nil {
			return nil, err
		}
	}
	return []byte(v), nil
}

// printVAPID implements `boop vapid`: print a fresh key pair as env lines.
func printVAPID() error {
	pub, priv, err := webpush.GenerateKeys()
	if err != nil {
		return err
	}
	fmt.Println("BOOP_VAPID_PUBLIC_KEY=" + pub)
	fmt.Println("BOOP_VAPID_PRIVATE_KEY=" + priv)
	return nil
}
