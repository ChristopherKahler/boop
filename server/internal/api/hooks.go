package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/chrisgreg/boop/server/internal/intake"
)

// hookMaxBody caps an incoming webhook (Sentry alert payloads with stack
// traces are the largest).
const hookMaxBody = 2 << 20

// hook receives a monitoring service's webhook: /hooks/{kind}/{token}. This is
// the only path meant to be reachable from the internet (the rest of the hub
// can stay private); the token in the address is the credential.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	kind, token := r.PathValue("kind"), r.PathValue("token")
	in, err := s.Intakes.Authenticate(r.Context(), kind, token)
	if err != nil {
		// Same answer for a wrong kind or token: nothing to probe.
		writeError(w, http.StatusNotFound, "not_found", "no such intake")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hookMaxBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "webhook body must be 2 MB or smaller")
		return
	}
	res, err := intake.Map(intake.Request{
		Kind: kind, Method: r.Method, Header: r.Header, Query: r.URL.Query(), Body: body,
		Token: token, Secret: in.Secret(), Mapping: in.Mapping,
	})
	switch {
	case errors.Is(err, intake.ErrSignature):
		s.Log.Warn("hook.bad_signature", "intake_id", in.ID, "kind", kind)
		_ = s.Intakes.Received(r.Context(), in.ID, "rejected: "+err.Error())
		writeError(w, http.StatusUnauthorized, "bad_signature", err.Error())
		return
	case err != nil:
		s.Log.Warn("hook.rejected", "intake_id", in.ID, "kind", kind, "error", err.Error())
		_ = s.Intakes.Received(r.Context(), in.ID, "rejected: "+err.Error())
		writeError(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	p, err := s.Projects.Get(r.Context(), in.ProjectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	type stored struct {
		ID      string `json:"id"`
		AlertID string `json:"alert_id,omitempty"`
		Alert   string `json:"alert,omitempty"`
		Route   string `json:"route"`
	}
	out := []stored{}
	var problems []string
	for _, ev := range res.Events {
		got, err := s.ingestAndEnqueue(r.Context(), p, ev, "hook:"+kind)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		st := stored{ID: got.Event.ID, Route: got.Decision.Reason, Alert: string(got.Change)}
		if got.Alert != nil {
			st.AlertID = got.Alert.ID
		}
		out = append(out, st)
	}
	result := fmt.Sprintf("%d event(s)", len(out))
	switch {
	case len(res.Events) == 0:
		result = "ignored: " + res.Ignored
	case len(problems) > 0:
		result += "; rejected: " + strings.Join(problems, "; ")
	}
	_ = s.Intakes.Received(r.Context(), in.ID, result)
	s.Log.Info("hook.received", "intake_id", in.ID, "kind", kind, "events", len(out), "ignored", res.Ignored)
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "ignored": res.Ignored, "rejected": problems})
}

// intakeURL is the address a service posts to. BOOP_INTAKE_URL wins (the
// hub's public, internet-reachable address); otherwise the hub's own URL.
func (s *Server) intakeURL(r *http.Request, kind, token string) string {
	base := s.Config.IntakeURL
	if base == "" {
		base = s.baseURL(r)
	}
	return strings.TrimRight(base, "/") + "/hooks/" + kind + "/" + token
}

type intakeCreated struct {
	intake.Intake
	Token string `json:"token"`
	URL   string `json:"url"`
}

func (s *Server) listIntakes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Intakes.List(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		s.fail(w, err)
		return
	}
	kinds := []map[string]string{}
	for _, k := range intake.Kinds {
		kinds = append(kinds, map[string]string{"kind": k, "label": intake.KindLabel[k]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"intakes": list, "kinds": kinds})
}

func (s *Server) createIntake(w http.ResponseWriter, r *http.Request) {
	var in intake.Input
	if !readJSON(w, r, &in) {
		return
	}
	p, err := s.Projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	got, token, err := s.Intakes.Create(r.Context(), p.ID, in)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("intake.created", "intake_id", got.ID, "kind", got.Kind, "project_id", p.ID)
	writeJSON(w, http.StatusCreated, intakeCreated{Intake: got, Token: token, URL: s.intakeURL(r, got.Kind, token)})
}

func (s *Server) updateIntake(w http.ResponseWriter, r *http.Request) {
	var in intake.Input
	if !readJSON(w, r, &in) {
		return
	}
	got, err := s.Intakes.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) rotateIntake(w http.ResponseWriter, r *http.Request) {
	token, err := s.Intakes.RotateToken(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	got, err := s.Intakes.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("intake.rotated", "intake_id", got.ID)
	writeJSON(w, http.StatusOK, intakeCreated{Intake: got, Token: token, URL: s.intakeURL(r, got.Kind, token)})
}

func (s *Server) deleteIntake(w http.ResponseWriter, r *http.Request) {
	if err := s.Intakes.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("intake.deleted", "intake_id", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// previewIntake shows what a sample payload would become, storing nothing:
// the way to build and check a generic mapping before pointing a service at it.
func (s *Server) previewIntake(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind    string            `json:"kind"`
		Mapping json.RawMessage   `json:"mapping"`
		Payload json.RawMessage   `json:"payload"`
		Headers map[string]string `json:"headers"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !intake.ValidKind(in.Kind) {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "unknown kind")
		return
	}
	if in.Kind == intake.KindGeneric {
		var m intake.Mapping
		if err := json.Unmarshal(in.Mapping, &m); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "mapping: "+err.Error())
			return
		}
		if err := m.Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid", "mapping: "+err.Error())
			return
		}
	}
	h := http.Header{}
	for k, v := range in.Headers {
		h.Set(k, v)
	}
	res, err := intake.Map(intake.Request{Kind: in.Kind, Method: "POST", Header: h, Body: in.Payload, Mapping: in.Mapping})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": res.Events, "ignored": res.Ignored})
}
