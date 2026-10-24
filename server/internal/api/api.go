// Package api exposes the Boop HTTP API and wires it to the domain packages.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chrisgreg/boop/server/internal/alerts"
	"github.com/chrisgreg/boop/server/internal/apns"
	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/clients"
	"github.com/chrisgreg/boop/server/internal/config"
	"github.com/chrisgreg/boop/server/internal/delivery"
	"github.com/chrisgreg/boop/server/internal/devices"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/events/redact"
	"github.com/chrisgreg/boop/server/internal/intake"
	"github.com/chrisgreg/boop/server/internal/mcp"
	"github.com/chrisgreg/boop/server/internal/pairing"
	"github.com/chrisgreg/boop/server/internal/projects"
	"github.com/chrisgreg/boop/server/internal/ratelimit"
	"github.com/chrisgreg/boop/server/internal/routing"
	"github.com/chrisgreg/boop/server/internal/settings"
	"github.com/chrisgreg/boop/server/internal/silences"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

// Version is the server version, overridden at build time via -ldflags.
var Version = "1.3.0"

// Server holds every dependency the handlers need.
type Server struct {
	Config     config.Config
	DB         *sql.DB
	Log        *slog.Logger
	Settings   *settings.Store
	Clients    *clients.Store
	Projects   *projects.Store
	Alerts     *alerts.Store
	Intakes    *intake.Store
	Devices    *devices.Store
	Pairing    *pairing.Store
	Events     *events.Store
	Silences   *silences.Store
	Dispatcher *delivery.Dispatcher
	APNS       *apns.Client // nil when not configured
	APNSError  string       // why APNS is nil
	// WebPush sends to the installed web app; nil when its keys are invalid.
	WebPush      *webpush.Client
	WebPushError string
	// ActionSecret signs the Acknowledge tokens carried in pushes.
	ActionSecret []byte
	Admin        *auth.Admin // web UI / admin API login; open when not enabled
	// HookAddresses limits requests to each webhook address and HookSenders
	// requests from each sender to all of them; nil means no limit.
	HookAddresses *ratelimit.Limiter
	HookSenders   *ratelimit.Limiter
	StartedAt     time.Time
	Web           http.Handler
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /manifest.webmanifest", s.manifest)

	// Admin session.
	mux.HandleFunc("GET /api/v1/auth/me", s.authMe)
	mux.HandleFunc("POST /api/v1/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.authLogout)
	mux.HandleFunc("POST /api/v1/auth/setup", s.authSetup)

	// Event ingestion: project API key.
	mux.Handle("POST /api/v1/events", s.projectAuth(s.createEvent))

	// Sentry SDK compatibility: point a Sentry DSN at Boop and existing Sentry
	// clients report here. The DSN public key is a Boop project API key; the
	// numeric project id in the path is ignored.
	mux.HandleFunc("POST /api/{project_id}/envelope/{$}", s.sentryEnvelope)

	// Reading events: device credential or the (unauthenticated) web UI.
	mux.Handle("GET /api/v1/events", s.readerAuth(s.listEvents))
	mux.Handle("GET /api/v1/events/{id}", s.readerAuth(s.getEvent))
	mux.Handle("GET /api/v1/events/{id}/deliveries", s.readerAuth(s.eventDeliveries))

	// Devices: the paired phone manages itself; the web UI lists/removes.
	mux.Handle("POST /api/v1/devices", s.deviceAuth(s.registerDevice))
	mux.Handle("PATCH /api/v1/devices/{id}", s.deviceOrAdmin(s.updateDevice))
	mux.Handle("DELETE /api/v1/devices/{id}", s.deviceOrAdmin(s.deleteDevice))
	mux.Handle("GET /api/v1/devices", s.adminAuth(s.listDevices))

	// Pairing.
	mux.Handle("POST /api/v1/pairing", s.adminAuth(s.createPairing))
	mux.Handle("GET /api/v1/pairing", s.adminAuth(s.listPairing))
	mux.Handle("DELETE /api/v1/pairing/{id}", s.adminAuth(s.revokePairing))
	mux.HandleFunc("POST /api/v1/pairing/exchange", s.exchangePairing)

	// Alerts: one row per problem, with acknowledge and resolve. Readable and
	// actionable by the web UI or a paired device.
	mux.Handle("GET /api/v1/alerts", s.readerAuth(s.listAlerts))
	mux.Handle("GET /api/v1/alerts/{id}", s.readerAuth(s.getAlert))
	mux.Handle("POST /api/v1/alerts/{id}/acknowledge", s.readerAuth(s.acknowledgeAlert))
	mux.Handle("POST /api/v1/alerts/{id}/resolve", s.readerAuth(s.resolveAlert))
	mux.Handle("POST /api/v1/alerts/{id}/reopen", s.readerAuth(s.reopenAlert))

	// Web Push: the installed web app subscribes here, and a notification's
	// Acknowledge button posts its signed token to push/action (no login).
	mux.Handle("GET /api/v1/webpush", s.adminAuth(s.webPushInfo))
	mux.Handle("POST /api/v1/webpush/subscriptions", s.adminAuth(s.webPushSubscribe))
	mux.Handle("DELETE /api/v1/webpush/subscriptions", s.adminAuth(s.webPushUnsubscribe))
	mux.HandleFunc("POST /api/v1/push/action", s.pushAction)

	// Intakes: webhook addresses for monitoring services. /hooks/ is the only
	// part of the hub that has to be reachable from the internet.
	mux.HandleFunc("POST /hooks/{kind}/{token}", s.hook)
	mux.HandleFunc("GET /hooks/{kind}/{token}", s.hook)
	// /hooks is the one path published to the internet, so anything else
	// under it gets the wrong-address answer, never the web UI.
	hookNotFound := func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such intake")
	}
	mux.HandleFunc("/hooks", hookNotFound)
	mux.HandleFunc("/hooks/", hookNotFound)
	mux.Handle("GET /api/v1/intakes", s.adminAuth(s.listIntakes))
	mux.Handle("POST /api/v1/projects/{id}/intakes", s.adminAuth(s.createIntake))
	mux.Handle("PATCH /api/v1/intakes/{id}", s.adminAuth(s.updateIntake))
	mux.Handle("POST /api/v1/intakes/{id}/rotate", s.adminAuth(s.rotateIntake))
	mux.Handle("DELETE /api/v1/intakes/{id}", s.adminAuth(s.deleteIntake))
	mux.Handle("POST /api/v1/intakes/preview", s.adminAuth(s.previewIntake))

	// Clients: the level above projects, carrying the default routing.
	mux.Handle("GET /api/v1/clients", s.adminAuth(s.listClients))
	mux.Handle("POST /api/v1/clients", s.adminAuth(s.createClient))
	mux.Handle("GET /api/v1/clients/{id}", s.adminAuth(s.getClient))
	mux.Handle("PATCH /api/v1/clients/{id}", s.adminAuth(s.updateClient))
	mux.Handle("DELETE /api/v1/clients/{id}", s.adminAuth(s.deleteClient))
	mux.Handle("GET /api/v1/clients/{id}/summary", s.adminAuth(s.clientSummary))
	mux.Handle("GET /api/v1/clients/{id}/summary.md", s.adminAuth(s.clientSummaryMarkdown))

	// Admin.
	mux.Handle("GET /api/v1/projects", s.adminAuth(s.listProjects))
	mux.HandleFunc("GET /api/v1/projects/icons", s.projectIcons)
	mux.Handle("POST /api/v1/projects", s.adminAuth(s.createProject))
	mux.Handle("GET /api/v1/projects/{id}", s.adminAuth(s.getProject))
	mux.Handle("PATCH /api/v1/projects/{id}", s.adminAuth(s.updateProject))
	mux.Handle("DELETE /api/v1/projects/{id}", s.adminAuth(s.deleteProject))
	mux.Handle("POST /api/v1/projects/{id}/rotate-key", s.adminAuth(s.rotateProjectKey))
	mux.Handle("POST /api/v1/events/{id}/unsilence", s.adminAuth(s.unsilenceEvent))
	mux.Handle("GET /api/v1/silences/{id}", s.adminAuth(s.getSilence))
	mux.Handle("GET /api/v1/silences", s.adminAuth(s.listSilences))
	mux.Handle("POST /api/v1/silences", s.adminAuth(s.createSilence))
	mux.Handle("DELETE /api/v1/silences/{id}", s.adminAuth(s.deleteSilence))
	mux.Handle("GET /api/v1/status", s.adminAuth(s.status))
	mux.Handle("GET /api/v1/settings", s.adminAuth(s.getSettings))
	mux.Handle("PATCH /api/v1/settings", s.adminAuth(s.updateSettings))
	mux.Handle("POST /api/v1/test", s.adminAuth(s.testNotification))

	// MCP (read-only) for AI agents: Streamable HTTP at /mcp.
	mux.Handle("/mcp", s.mcpAuth(mcp.Handler(mcp.NewServer(mcp.Stores{Projects: s.Projects, Events: s.Events}, Version), s.Log)))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	if s.Web != nil {
		mux.Handle("/", s.Web)
	}
	return s.logging(mux)
}

// ---- middleware ----

type ctxKey int

const (
	ctxProject ctxKey = iota
	ctxDevice
)

func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/health" {
			s.Log.Debug("http.request", "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// projectAuth requires a project API key.
func (s *Server) projectAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := auth.Bearer(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a project API key is required: Authorization: Bearer boop_proj_...")
			return
		}
		p, err := s.Projects.Authenticate(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid project API key")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxProject, p)))
	})
}

// deviceAuth requires a device credential.
func (s *Server) deviceAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := auth.Bearer(r)
		if cred == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a device credential is required")
			return
		}
		d, err := s.Devices.Authenticate(r.Context(), cred)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device credential")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxDevice, d)))
	})
}

// readerAuth accepts either a valid device credential or no credential at all
// (the embedded web UI). Any other bearer token is rejected.
func (s *Server) readerAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := auth.Bearer(r)
		if cred == "" {
			if !s.Admin.Authorized(r) {
				writeError(w, http.StatusUnauthorized, "login_required", "sign in to the Boop web UI")
				return
			}
			next(w, r)
			return
		}
		d, err := s.Devices.Authenticate(r.Context(), cred)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device credential")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxDevice, d)))
	})
}

// mcpAuth guards the MCP endpoint. It accepts the configured BOOP_MCP_TOKEN,
// a device credential, or an admin session/HTTP Basic login (or nothing when
// admin auth is off, like the rest of the read API). Project API keys are
// refused: they are write-only ingestion credentials.
func (s *Server) mcpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if on, err := s.mcpEnabled(r.Context()); err != nil {
			s.fail(w, err)
			return
		} else if !on {
			writeError(w, http.StatusNotFound, "mcp_disabled", "the MCP endpoint is turned off in Settings")
			return
		}
		cred := auth.Bearer(r)
		switch {
		case cred == "":
			if !s.Admin.Authorized(r) {
				writeError(w, http.StatusUnauthorized, "unauthorized", "an MCP token (BOOP_MCP_TOKEN) or device credential is required: Authorization: Bearer ...")
				return
			}
		case s.Config.MCPToken != "" && auth.Equal(auth.Hash(cred), auth.Hash(s.Config.MCPToken)):
		case auth.HasPrefix(cred, auth.PrefixDevice):
			if _, err := s.Devices.Authenticate(r.Context(), cred); err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device credential")
				return
			}
		default:
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid MCP credential")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminAuth serves the web UI and admin API. When BOOP_ADMIN_USER/PASSWORD are
// set, a session cookie (from /auth/login) or HTTP Basic credentials are
// required; otherwise it is open and expected to sit behind the operator's own
// proxy. Project and device credentials are explicitly refused so that a
// leaked client secret grants no admin rights.
func (s *Server) adminAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cred := auth.Bearer(r); cred != "" {
			writeError(w, http.StatusForbidden, "forbidden", "project and device credentials cannot perform administrative actions")
			return
		}
		if !s.Admin.Authorized(r) {
			writeError(w, http.StatusUnauthorized, "login_required", "sign in to the Boop web UI")
			return
		}
		next(w, r)
	})
}

// ---- admin session ----

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	ok := s.Admin.Authorized(r)
	// Each visit renews the session, so the installed phone app stays signed
	// in as long as it is opened now and then.
	if c, err := r.Cookie(auth.SessionCookie); ok && err == nil && s.Admin.Renew(c.Value) {
		s.Admin.SetCookie(w, r, c.Value)
	}
	writeJSON(w, http.StatusOK, map[string]any{"auth_required": s.Admin.Enabled(), "authenticated": ok, "needs_setup": s.Admin.NeedsSetup()})
}

// tooMany answers 429 while an address is locked out after failed attempts.
func (s *Server) tooMany(w http.ResponseWriter, r *http.Request) bool {
	blocked, wait := s.Admin.Limiter().Blocked(auth.ClientIP(r))
	if !blocked {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts", "too many failed attempts; try again in "+wait.Round(time.Minute).String())
	return true
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	if !s.Admin.Enabled() {
		writeJSON(w, http.StatusOK, map[string]any{"auth_required": false, "authenticated": true})
		return
	}
	if s.Admin.NeedsSetup() {
		writeError(w, http.StatusConflict, "needs_setup", "no login exists yet: create one with the setup code from the server log")
		return
	}
	if s.tooMany(w, r) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	tok, ok := s.Admin.Login(in.Username, in.Password)
	if !ok {
		s.Admin.Limiter().Fail(auth.ClientIP(r))
		time.Sleep(400 * time.Millisecond)
		s.Log.Warn("auth.login_failed", "remote", auth.ClientIP(r))
		writeError(w, http.StatusUnauthorized, "bad_credentials", "wrong username or password")
		return
	}
	s.Admin.Limiter().Reset(auth.ClientIP(r))
	s.Admin.SetCookie(w, r, tok)
	s.Log.Info("auth.login", "remote", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"auth_required": true, "authenticated": true})
}

// authSetup creates the login on a fresh install. It needs the one-time setup
// code the server printed in its log, so a hub that is already public cannot
// be claimed by a stranger who finds it first.
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	if !s.Admin.NeedsSetup() {
		writeError(w, http.StatusConflict, "already_set_up", "a login already exists")
		return
	}
	if s.tooMany(w, r) {
		return
	}
	var in struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !s.Admin.CheckSetupCode(in.Code) {
		s.Admin.Limiter().Fail(auth.ClientIP(r))
		s.Log.Warn("auth.setup_bad_code", "remote", auth.ClientIP(r))
		writeError(w, http.StatusUnauthorized, "bad_setup_code", "that setup code is not right; it is in the server log, on the line auth.setup_required")
		return
	}
	user := strings.TrimSpace(in.Username)
	if user == "" || len(user) > 60 {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "the user name must be 1-60 characters")
		return
	}
	if err := auth.ValidatePassword(in.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if err := s.Settings.Set(ctx, settings.KeyAdminUser, user); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Settings.Set(ctx, settings.KeyAdminPasswordHash, hash); err != nil {
		s.fail(w, err)
		return
	}
	s.Admin.UseStoredLogin(user, hash)
	s.Admin.Limiter().Reset(auth.ClientIP(r))
	s.Admin.SetCookie(w, r, s.Admin.NewSession(r.UserAgent()))
	s.Log.Info("auth.setup_completed", "user", user, "remote", auth.ClientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"auth_required": true, "authenticated": true})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		s.Admin.Logout(c.Value)
	}
	s.Admin.ClearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// deviceOrAdmin allows a device to act on itself, or the web UI on anything.
func (s *Server) deviceOrAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := auth.Bearer(r)
		if cred == "" {
			if !s.Admin.Authorized(r) {
				writeError(w, http.StatusUnauthorized, "login_required", "sign in to the Boop web UI")
				return
			}
			next(w, r)
			return
		}
		d, err := s.Devices.Authenticate(r.Context(), cred)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device credential")
			return
		}
		if d.ID != r.PathValue("id") {
			writeError(w, http.StatusForbidden, "forbidden", "a device may only modify itself")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxDevice, d)))
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiError{Error: code, Message: msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "request body must be 1 MB or smaller")
		return false
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a JSON object")
		return false
	}
	if err := json.Unmarshal(b, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}

// fail maps domain errors to HTTP responses.
func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, events.ErrNotFound), errors.Is(err, devices.ErrNotFound), errors.Is(err, pairing.ErrNotFound), errors.Is(err, silences.ErrNotFound),
		errors.Is(err, clients.ErrNotFound), errors.Is(err, alerts.ErrNotFound), errors.Is(err, intake.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, projects.ErrInvalid), errors.Is(err, events.ErrInvalid), errors.Is(err, devices.ErrInvalid), errors.Is(err, silences.ErrInvalid),
		errors.Is(err, clients.ErrInvalid), errors.Is(err, alerts.ErrInvalid), errors.Is(err, intake.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	case errors.Is(err, clients.ErrInUse), errors.Is(err, alerts.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, pairing.ErrInvalidToken):
		writeError(w, http.StatusUnauthorized, "invalid_pairing_token", err.Error())
	default:
		s.Log.Error("http.error", "error", err.Error())
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong")
	}
}

func (s *Server) redactor(ctx context.Context) *redact.Redactor {
	extra, err := s.Settings.GetList(ctx, settings.KeyRedactKeys)
	if err != nil {
		s.Log.Error("settings.read_failed", "error", err.Error())
	}
	return redact.New(extra...)
}

// ---- health & status ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if err := s.DB.PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "database": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type statusResponse struct {
	Version       string        `json:"version"`
	Server        string        `json:"server"`
	Database      string        `json:"database"`
	DatabasePath  string        `json:"database_path"`
	BaseURL       string        `json:"base_url"`
	UptimeSeconds int64         `json:"uptime_seconds"`
	APNS          apnsStatus    `json:"apns"`
	WebPush       webPushStatus `json:"webpush"`
	// PushReady is true when any push channel (web push or APNs) can send.
	PushReady      bool               `json:"push_ready"`
	Devices        int                `json:"devices"`
	PushableDevice int                `json:"pushable_devices"`
	Projects       int                `json:"projects"`
	Events         int                `json:"events"`
	LastPush       *delivery.Delivery `json:"last_push"`
	RetentionDays  int                `json:"retention_days"`
	SetupCompleted bool               `json:"setup_completed"`
	AdminAuth      bool               `json:"admin_auth"`
}

type apnsStatus struct {
	Configured  bool     `json:"configured"`
	Error       string   `json:"error,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	TeamID      string   `json:"team_id,omitempty"`
	KeyID       string   `json:"key_id,omitempty"`
	BundleID    string   `json:"bundle_id,omitempty"`
	Environment string   `json:"environment"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := statusResponse{Version: Version, Server: "ok", Database: "ok", DatabasePath: s.Config.DatabasePath, BaseURL: s.baseURL(r), UptimeSeconds: int64(time.Since(s.StartedAt).Seconds()), AdminAuth: s.Admin.Enabled()}
	if err := s.DB.PingContext(ctx); err != nil {
		resp.Database = err.Error()
	}
	resp.APNS = apnsStatus{Configured: s.APNS != nil, Error: s.APNSError, Missing: s.Config.APNS.Missing(),
		TeamID: s.Config.APNS.TeamID, KeyID: s.Config.APNS.KeyID, BundleID: s.Config.APNS.BundleID, Environment: s.Config.APNS.Environment}
	resp.WebPush = s.webPushStatus()
	resp.PushReady = resp.WebPush.Configured || resp.APNS.Configured
	devs, err := s.Devices.List(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp.Devices = len(devs)
	for _, d := range devs {
		if d.HasToken {
			resp.PushableDevice++
		}
	}
	if resp.Projects, err = s.Projects.Count(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if resp.Events, err = s.Events.Count(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if resp.LastPush, err = s.Dispatcher.Last(ctx); err != nil {
		s.fail(w, err)
		return
	}
	if resp.RetentionDays, err = s.Settings.GetInt(ctx, settings.KeyRetentionDays, s.Config.RetentionDays); err != nil {
		s.fail(w, err)
		return
	}
	if resp.SetupCompleted, err = s.Settings.GetBool(ctx, settings.KeySetupCompleted); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// baseURL is the configured public URL, or the request's origin as a fallback.
func (s *Server) baseURL(r *http.Request) string {
	if s.Config.BaseURL != "" {
		return s.Config.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// ---- settings ----

type settingsResponse struct {
	RetentionDays  int      `json:"retention_days"`
	RedactKeys     []string `json:"redact_keys"`
	DefaultRedact  []string `json:"default_redact_keys"`
	SetupCompleted bool     `json:"setup_completed"`
	MCPEnabled     bool     `json:"mcp_enabled"`
	// MCPTokenSet reports whether BOOP_MCP_TOKEN is configured (never the token itself).
	MCPTokenSet bool `json:"mcp_token_set"`
}

type settingsInput struct {
	RetentionDays  *int      `json:"retention_days"`
	RedactKeys     *[]string `json:"redact_keys"`
	SetupCompleted *bool     `json:"setup_completed"`
	MCPEnabled     *bool     `json:"mcp_enabled"`
}

// mcpEnabled reads the MCP switch; it defaults to on.
func (s *Server) mcpEnabled(ctx context.Context) (bool, error) {
	v, err := s.Settings.Get(ctx, settings.KeyMCPEnabled, "true")
	return v != "false" && v != "0", err
}

func (s *Server) readSettings(ctx context.Context) (settingsResponse, error) {
	var out settingsResponse
	var err error
	if out.RetentionDays, err = s.Settings.GetInt(ctx, settings.KeyRetentionDays, s.Config.RetentionDays); err != nil {
		return out, err
	}
	if out.RedactKeys, err = s.Settings.GetList(ctx, settings.KeyRedactKeys); err != nil {
		return out, err
	}
	if out.RedactKeys == nil {
		out.RedactKeys = []string{}
	}
	out.DefaultRedact = redact.DefaultKeys
	if out.SetupCompleted, err = s.Settings.GetBool(ctx, settings.KeySetupCompleted); err != nil {
		return out, err
	}
	out.MCPTokenSet = s.Config.MCPToken != ""
	out.MCPEnabled, err = s.mcpEnabled(ctx)
	return out, err
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.readSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	if !readJSON(w, r, &in) {
		return
	}
	ctx := r.Context()
	if in.RetentionDays != nil {
		if *in.RetentionDays < 0 || *in.RetentionDays > 3650 {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "retention_days must be between 0 (unlimited) and 3650")
			return
		}
		if err := s.Settings.Set(ctx, settings.KeyRetentionDays, strconv.Itoa(*in.RetentionDays)); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.RedactKeys != nil {
		var clean []string
		for _, k := range *in.RedactKeys {
			if k = strings.TrimSpace(k); k != "" {
				if strings.Contains(k, ",") {
					writeError(w, http.StatusUnprocessableEntity, "invalid", "redact_keys entries may not contain commas")
					return
				}
				clean = append(clean, k)
			}
		}
		if err := s.Settings.Set(ctx, settings.KeyRedactKeys, strings.Join(clean, ",")); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.SetupCompleted != nil {
		if err := s.Settings.Set(ctx, settings.KeySetupCompleted, strconv.FormatBool(*in.SetupCompleted)); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.MCPEnabled != nil {
		if err := s.Settings.Set(ctx, settings.KeyMCPEnabled, strconv.FormatBool(*in.MCPEnabled)); err != nil {
			s.fail(w, err)
			return
		}
		s.Log.Info("settings.mcp", "enabled", *in.MCPEnabled)
	}
	out, err := s.readSettings(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- events ----

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	p := r.Context().Value(ctxProject).(projects.Project)
	var in events.Input
	if !readJSON(w, r, &in) {
		return
	}
	res, err := s.ingestAndEnqueue(r.Context(), p, in, "api")
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"id": res.Event.ID, "created_at": res.Event.CreatedAt, "pushed": res.Decision.Push, "route": res.Decision.Reason}
	if res.Alert != nil {
		out["alert_id"] = res.Alert.ID
		out["alert"] = string(res.Change)
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := events.Filter{ProjectID: q.Get("project"), ClientID: q.Get("client"), AlertID: q.Get("alert"), Level: q.Get("level"), Source: q.Get("source"), Fingerprint: q.Get("fingerprint"), Before: q.Get("before"),
		Since: q.Get("since"), Until: q.Get("until"), Grouped: q.Get("grouped") == "true" || q.Get("grouped") == "1"}
	if f.Level != "" && !levels.Valid(f.Level) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "level must be one of "+strings.Join(levels.All, ", "))
		return
	}
	if v := q.Get("silenced"); v != "" {
		b := v == "true" || v == "1"
		f.Silenced = &b
	}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	page, err := s.Events.List(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	e, err := s.Events.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) eventDeliveries(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Events.Get(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	d, err := s.Dispatcher.ForEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": d})
}

// applySilence marks e as silenced when a rule matches and reports whether it did.
func (s *Server) applySilence(ctx context.Context, e *events.Event) bool {
	rule, err := s.Silences.Match(ctx, e.ProjectID, e.Fingerprint, e.Title, e.Source)
	if err != nil {
		s.Log.Error("silence.match_failed", "error", err.Error())
		return false
	}
	if rule == nil {
		return false
	}
	if err := s.Events.SetSilence(ctx, e.ID, rule.ID); err != nil {
		s.Log.Error("silence.record_failed", "error", err.Error())
	}
	e.SilenceID, e.Silenced = rule.ID, true
	s.Log.Info("event.silenced", "event_id", e.ID, "silence_id", rule.ID)
	return true
}

// ---- silences ----

// unsilenceEvent clears the flag and pushes the event now.
func (s *Server) unsilenceEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, err := s.Events.Get(ctx, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !e.Silenced {
		writeError(w, http.StatusUnprocessableEntity, "not_silenced", "this event was not silenced")
		return
	}
	p, err := s.Projects.Get(ctx, e.ProjectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Events.ClearSilence(ctx, e.ID); err != nil {
		s.fail(w, err)
		return
	}
	e.SilenceID, e.Silenced = "", false
	// Route it as the alert it belongs to would have been when it opened.
	att := alerts.Attachment{Change: alerts.None}
	if e.AlertID != "" {
		if a, err := s.Alerts.Get(ctx, e.AlertID); err == nil {
			att = alerts.Attachment{Alert: &a, Change: alerts.Opened}
		}
	}
	res := s.route(ctx, p, e, att, false)
	e = res.Event
	dl := []delivery.Delivery{}
	if res.Message != nil {
		if got := s.Dispatcher.Deliver(ctx, *res.Message); got != nil {
			dl = got
		}
	}
	s.Log.Info("event.unsilenced", "event_id", e.ID)
	writeJSON(w, http.StatusOK, map[string]any{"event": e, "deliveries": dl})
}

func (s *Server) getSilence(w http.ResponseWriter, r *http.Request) {
	sil, err := s.Silences.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sil)
}

func (s *Server) listSilences(w http.ResponseWriter, r *http.Request) {
	list, err := s.Silences.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.Events.CountSilenced(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"silences": list, "fields": silences.Fields, "silenced_events": n})
}

func (s *Server) createSilence(w http.ResponseWriter, r *http.Request) {
	var in silences.Input
	if !readJSON(w, r, &in) {
		return
	}
	sil, err := s.Silences.Create(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("silence.created", "silence_id", sil.ID, "field", sil.Field)
	writeJSON(w, http.StatusCreated, sil)
}

func (s *Server) deleteSilence(w http.ResponseWriter, r *http.Request) {
	if err := s.Silences.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("silence.deleted", "silence_id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// ---- projects ----

func (s *Server) projectIcons(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"shapes": projects.IconShapes, "colors": projects.IconColors})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Projects.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": ps})
}

type projectCreated struct {
	projects.Project
	APIKey string `json:"api_key"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var in projects.Input
	if !readJSON(w, r, &in) {
		return
	}
	if !s.resolveClient(w, r, &in.ClientID, true) {
		return
	}
	p, key, err := s.Projects.Create(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("project.created", "project_id", p.ID, "slug", p.Slug)
	writeJSON(w, http.StatusCreated, projectCreated{Project: p, APIKey: key})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	var in projects.Input
	if !readJSON(w, r, &in) {
		return
	}
	if !s.resolveClient(w, r, &in.ClientID, false) {
		return
	}
	p, err := s.Projects.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := s.Projects.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("project.deleted", "project_id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateProjectKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.Projects.RotateKey(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	p, err := s.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("project.key_rotated", "project_id", p.ID)
	writeJSON(w, http.StatusOK, projectCreated{Project: p, APIKey: key})
}

// ---- devices ----

func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	d := r.Context().Value(ctxDevice).(devices.Device)
	var in devices.Registration
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Devices.Register(r.Context(), d.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("device.registered", "device_id", d.ID, "push_registered", d.HasToken)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var in devices.Registration
	if !readJSON(w, r, &in) {
		return
	}
	d, err := s.Devices.Register(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.Devices.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("device.removed", "device_id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Devices.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": ds})
}

// ---- pairing ----

type pairingCreated struct {
	pairing.Token
	QR pairing.QRPayload `json:"qr"`
}

func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	t, err := s.Pairing.Create(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("pairing.created", "pairing_id", t.ID)
	writeJSON(w, http.StatusCreated, pairingCreated{Token: t, QR: pairing.QRPayload{Version: 1, Server: s.baseURL(r), Token: t.Raw}})
}

func (s *Server) listPairing(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Pairing.Pending(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairing_tokens": ts})
}

func (s *Server) revokePairing(w http.ResponseWriter, r *http.Request) {
	if err := s.Pairing.Revoke(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) exchangePairing(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	res, err := s.Pairing.Exchange(r.Context(), in.Token, in.Name, in.Platform)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("pairing.completed", "device_id", res.Device.ID)
	writeJSON(w, http.StatusCreated, res)
}

// ---- test notification ----

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		ProjectID string `json:"project_id"`
		// Force pushes even when routing would keep a success event in the
		// inbox; the web UI's "Send test push" button sets it.
		Force bool `json:"force"`
	}
	if r.ContentLength != 0 {
		if !readJSON(w, r, &in) {
			return
		}
	}
	var p projects.Project
	var err error
	if in.ProjectID != "" {
		p, err = s.Projects.Get(ctx, in.ProjectID)
	} else {
		p, err = s.Projects.First(ctx)
		if errors.Is(err, projects.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "no_project", "create a project before sending a test notification")
			return
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.ingest(ctx, p, events.Input{
		Title: "Test boop", Body: "If you can read this on your phone, Boop is working.", Level: levels.Success, Source: "boop",
		Data: json.RawMessage(`{"test":true}`),
	}, "test")
	if err != nil {
		s.fail(w, err)
		return
	}
	if res.Message == nil && in.Force && !res.Event.Silenced {
		c, _ := s.Clients.Get(ctx, p.ClientID)
		res.Message = &delivery.Message{Event: res.Event, Project: p, ClientName: c.Name, Kind: routing.KindEvent}
		res.Event.Route = "pushed: test push (forced)"
		_ = s.Events.SetRoute(ctx, res.Event.ID, res.Event.Route)
	}
	dl := []delivery.Delivery{}
	if res.Message != nil {
		if got := s.Dispatcher.Deliver(ctx, *res.Message); got != nil {
			dl = got
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"event": res.Event, "deliveries": dl, "apns_configured": s.APNS != nil, "route": res.Event.Route})
}
