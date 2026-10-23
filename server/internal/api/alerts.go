package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chrisgreg/boop/server/internal/alerts"
	"github.com/chrisgreg/boop/server/internal/clients"
	"github.com/chrisgreg/boop/server/internal/devices"
)

// ---- clients ----

func (s *Server) listClients(w http.ResponseWriter, r *http.Request) {
	cs, err := s.Clients.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": cs})
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var in clients.Input
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Clients.Create(r.Context(), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("client.created", "client_id", c.ID, "slug", c.Slug)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) getClient(w http.ResponseWriter, r *http.Request) {
	c, err := s.Clients.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) updateClient(w http.ResponseWriter, r *http.Request) {
	var in clients.Input
	if !readJSON(w, r, &in) {
		return
	}
	c, err := s.Clients.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteClient(w http.ResponseWriter, r *http.Request) {
	if err := s.Clients.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("client.deleted", "client_id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// resolveClient turns a client id or slug in *id into the client's id. When
// the field is absent and useDefault is set, the default (oldest) client is
// used. It writes the error response and returns false on failure.
func (s *Server) resolveClient(w http.ResponseWriter, r *http.Request, id **string, useDefault bool) bool {
	var c clients.Client
	var err error
	switch {
	case *id != nil && strings.TrimSpace(**id) != "":
		c, err = s.Clients.Get(r.Context(), strings.TrimSpace(**id))
		if errors.Is(err, clients.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "invalid project: unknown client")
			return false
		}
	case useDefault:
		c, err = s.Clients.Default(r.Context())
		if errors.Is(err, clients.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "invalid project: create a client first, then pass its client_id")
			return false
		}
	default:
		return true
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	*id = &c.ID
	return true
}

// ---- alerts ----

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := alerts.Filter{Status: q.Get("status"), ClientID: q.Get("client"), ProjectID: q.Get("project"), Level: q.Get("level"), Before: q.Get("before")}
	if l := q.Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n <= 0 {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "limit must be a positive integer")
			return
		}
		f.Limit = n
	}
	page, err := s.Alerts.List(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	open, acked, err := s.Alerts.Counts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": page.Alerts, "next_cursor": page.NextCursor, "open": open, "acknowledged": acked})
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.Alerts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// actor names who acted on an alert: the paired device, or the web UI (which
// tells us when it is the installed phone app).
func actor(r *http.Request) string {
	if d, ok := r.Context().Value(ctxDevice).(devices.Device); ok {
		return "device: " + d.Name
	}
	if by := strings.TrimSpace(r.URL.Query().Get("by")); by == "phone" {
		return "phone"
	}
	return "web"
}

func (s *Server) acknowledgeAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.Alerts.Acknowledge(r.Context(), r.PathValue("id"), actor(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("alert.acknowledged", "alert_id", a.ID, "by", a.AcknowledgedBy)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) resolveAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.Alerts.Resolve(r.Context(), r.PathValue("id"), actor(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("alert.resolved", "alert_id", a.ID, "by", a.ResolvedBy)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) reopenAlert(w http.ResponseWriter, r *http.Request) {
	a, err := s.Alerts.Reopen(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("alert.reopened", "alert_id", a.ID)
	writeJSON(w, http.StatusOK, a)
}
