package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

type webPushStatus struct {
	Configured bool   `json:"configured"`
	PublicKey  string `json:"public_key,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (s *Server) webPushStatus() webPushStatus {
	if s.WebPush == nil {
		return webPushStatus{Error: s.WebPushError}
	}
	return webPushStatus{Configured: true, PublicKey: s.WebPush.PublicKey()}
}

// webPushInfo gives the web app the key it subscribes with.
func (s *Server) webPushInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.webPushStatus())
}

// webPushSubscribe stores this browser's push subscription as a device.
func (s *Server) webPushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.WebPush == nil {
		writeError(w, http.StatusServiceUnavailable, "webpush_unavailable", "web push is not configured on this server: "+s.WebPushError)
		return
	}
	var in struct {
		Subscription webpush.Subscription `json:"subscription"`
		Name         string               `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.Subscription.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", "invalid subscription: "+err.Error())
		return
	}
	keys, _ := json.Marshal(in.Subscription.Keys)
	d, err := s.Devices.SaveWebSubscription(r.Context(), in.Name, in.Subscription.Endpoint, string(keys))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("device.webpush_subscribed", "device_id", d.ID, "name", d.Name)
	writeJSON(w, http.StatusCreated, d)
}

// webPushUnsubscribe forgets this browser's subscription.
func (s *Server) webPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.Devices.DeleteByToken(r.Context(), in.Endpoint); err != nil {
		s.fail(w, err)
		return
	}
	s.Log.Info("device.webpush_unsubscribed")
	w.WriteHeader(http.StatusNoContent)
}

// pushAction performs the action a notification button carries: the token
// was signed by this server for one alert and expires after a week.
func (s *Server) pushAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	alertID, action, err := auth.VerifyAction(s.ActionSecret, in.Token, time.Now())
	if err != nil || len(s.ActionSecret) == 0 {
		writeError(w, http.StatusUnauthorized, "invalid_token", auth.ErrBadActionToken.Error())
		return
	}
	switch action {
	case "acknowledge":
		a, err := s.Alerts.Acknowledge(r.Context(), alertID, "phone (notification)")
		if err != nil {
			s.fail(w, err)
			return
		}
		s.Log.Info("alert.acknowledged", "alert_id", a.ID, "by", a.AcknowledgedBy)
		writeJSON(w, http.StatusOK, a)
	default:
		writeError(w, http.StatusUnprocessableEntity, "invalid", "unknown action")
	}
}

// manifest describes the installable web app. It is served by the server so
// the home-screen name comes from configuration (BOOP_APP_NAME).
func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	name := s.Config.AppName
	if name == "" {
		name = "Boop"
	}
	short := name
	if len([]rune(short)) > 12 {
		short = string([]rune(short)[:12])
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name": name, "short_name": short, "id": "/", "start_url": "/", "scope": "/",
		"display": "standalone", "orientation": "portrait",
		"background_color": "#FDFDFE", "theme_color": "#7C83E8",
		"description": "Alerts from the services watching your systems, grouped by client.",
		"icons": []map[string]string{
			{"src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	})
}
