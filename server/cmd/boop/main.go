// Command boop runs the Boop notification server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/chrisgreg/boop/server/internal/alerts"
	"github.com/chrisgreg/boop/server/internal/api"
	"github.com/chrisgreg/boop/server/internal/apns"
	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/clients"
	"github.com/chrisgreg/boop/server/internal/config"
	"github.com/chrisgreg/boop/server/internal/database"
	"github.com/chrisgreg/boop/server/internal/delivery"
	"github.com/chrisgreg/boop/server/internal/devices"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/intake"
	"github.com/chrisgreg/boop/server/internal/pairing"
	"github.com/chrisgreg/boop/server/internal/projects"
	"github.com/chrisgreg/boop/server/internal/ratelimit"
	"github.com/chrisgreg/boop/server/internal/settings"
	"github.com/chrisgreg/boop/server/internal/silences"
	webui "github.com/chrisgreg/boop/server/internal/web"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "vapid" {
		if err := printVAPID(); err != nil {
			fmt.Fprintln(os.Stderr, "boop:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "boop:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	db, err := database.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", cfg.DatabasePath, err)
	}
	defer db.Close()

	st := settings.New(db)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Retention: an explicit BOOP_RETENTION_DAYS wins; otherwise the UI-saved value (default 90) is kept.
	if cfg.RetentionDaysSet {
		err = st.Set(ctx, settings.KeyRetentionDays, strconv.Itoa(cfg.RetentionDays))
	} else {
		err = st.SetDefault(ctx, settings.KeyRetentionDays, strconv.Itoa(cfg.RetentionDays))
	}
	if err != nil {
		return err
	}

	devStore := devices.New(db)
	var sender delivery.Sender
	var client *apns.Client
	apnsErr := ""
	if cfg.APNS.Configured() {
		client, err = apns.New(cfg.APNS)
		if err != nil {
			apnsErr = err.Error()
			log.Warn("apns.invalid", "error", apnsErr)
		} else {
			sender = client
			log.Info("apns.configured", "environment", cfg.APNS.Environment, "bundle_id", cfg.APNS.BundleID)
		}
	} else {
		apnsErr = "missing " + join(cfg.APNS.Missing())
		log.Warn("apns.not_configured", "missing", cfg.APNS.Missing())
	}
	dispatcher := delivery.New(db, devStore, sender, log)
	secret, err := actionSecret(ctx, st)
	if err != nil {
		return err
	}
	var web *webpush.Client
	webErr := ""
	if keys, source, err := webPushKeys(ctx, cfg, st); err != nil {
		webErr = err.Error()
		log.Warn("webpush.invalid", "error", webErr)
	} else {
		web = webpush.NewClient(keys, webPushSubject(cfg))
		dispatcher.SetWeb(web, func(alertID string) string {
			return auth.SignAction(secret, alertID, "acknowledge", time.Now().Add(7*24*time.Hour))
		})
		log.Info("webpush.configured", "keys", source, "subject", webPushSubject(cfg))
	}
	dispatcher.Start(ctx)

	admin := auth.NewAdmin(cfg.AdminUser, cfg.AdminPassword)
	sessions := auth.NewDBSessions(db)
	admin.UseSessions(sessions)
	if err := setupLogin(ctx, log, cfg, st, admin); err != nil {
		return err
	}

	evStore := events.New(db)
	srv := &api.Server{
		Config: cfg, DB: db, Log: log, Settings: st,
		Clients: clients.New(db), Projects: projects.New(db), Alerts: alerts.New(db), Intakes: intake.New(db), Devices: devStore, Pairing: pairing.New(db, devStore), Events: evStore, Silences: silences.New(db),
		Dispatcher: dispatcher, APNS: client, APNSError: apnsErr, WebPush: web, WebPushError: webErr, ActionSecret: secret,
		Admin: admin, StartedAt: time.Now(), Web: webui.Handler(),
		HookAddresses: ratelimit.New(cfg.HookLimit), HookSenders: ratelimit.New(cfg.HookSenderLimit),
	}

	go retentionLoop(ctx, log, st, evStore, srv.Pairing, sessions, cfg.RetentionDays)

	httpServer := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Info("server.started", "port", cfg.Port, "database", cfg.DatabasePath, "version", api.Version)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("server.stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	stop()
	dispatcher.Wait()
	return nil
}

// retentionLoop prunes old events once at start and then hourly.
func retentionLoop(ctx context.Context, log *slog.Logger, st *settings.Store, ev *events.Store, pr *pairing.Store, sessions *auth.DBSessions, def int) {
	run := func() {
		days, err := st.GetInt(ctx, settings.KeyRetentionDays, def)
		if err != nil {
			log.Error("retention.failed", "error", err.Error())
			return
		}
		n, err := ev.Prune(ctx, days, time.Now())
		if err != nil {
			log.Error("retention.failed", "error", err.Error())
			return
		}
		_ = pr.Cleanup(ctx)
		_ = sessions.Prune(time.Now())
		if n > 0 {
			log.Info("retention.completed", "deleted", n, "retention_days", days)
		}
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// setupLogin picks where the login comes from: BOOP_ADMIN_USER/PASSWORD, a
// login created earlier in the setup screen, or (on a fresh install) a
// one-time setup code printed here that the first visitor must enter.
func setupLogin(ctx context.Context, log *slog.Logger, cfg config.Config, st *settings.Store, admin *auth.Admin) error {
	if admin.Enabled() {
		log.Info("auth.enabled", "user", cfg.AdminUser, "source", "environment")
		return nil
	}
	user, err := st.Get(ctx, settings.KeyAdminUser, "")
	if err != nil {
		return err
	}
	hash, err := st.Get(ctx, settings.KeyAdminPasswordHash, "")
	if err != nil {
		return err
	}
	switch {
	case user != "" && hash != "":
		admin.UseStoredLogin(user, hash)
		log.Info("auth.enabled", "user", user, "source", "setup")
	case cfg.AllowNoLogin:
		log.Warn("auth.disabled", "hint", "BOOP_ALLOW_NO_LOGIN=true: anyone who can reach this server can use it")
	default:
		code := auth.NewSetupCode()
		admin.RequireSetup(code)
		log.Warn("auth.setup_required", "setup_code", code, "hint", "open the hub in a browser and enter this code to create your login")
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
