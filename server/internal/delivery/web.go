package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/chrisgreg/boop/server/internal/devices"
	"github.com/chrisgreg/boop/server/internal/events/levels"
	"github.com/chrisgreg/boop/server/internal/routing"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

// WebPayload is the JSON the service worker turns into a notification.
type WebPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Tag groups notifications per alert: a repeat, escalation or "resolved"
	// replaces the alert's earlier notification instead of stacking.
	Tag                string      `json:"tag"`
	Renotify           bool        `json:"renotify"`
	RequireInteraction bool        `json:"require_interaction"`
	URL                string      `json:"url"`
	Level              string      `json:"level"`
	Kind               string      `json:"kind"`
	AlertID            string      `json:"alert_id,omitempty"`
	EventID            string      `json:"event_id"`
	Ack                string      `json:"ack,omitempty"` // token for the Acknowledge button
	Actions            []WebAction `json:"actions,omitempty"`
	Timestamp          int64       `json:"ts"`
}

// WebAction is a notification button: "ack", or "linkN" opening URL.
type WebAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
}

// BuildWebPayload renders m for the service worker, keeping it inside one
// Web Push record (long bodies and links are shed first).
func BuildWebPayload(m Message, ackToken string, now time.Time) []byte {
	title, body := Content(m)
	e := m.Event
	p := WebPayload{
		Title: title, Body: body, Level: e.Level, Kind: m.Kind, EventID: e.ID, Timestamp: now.UnixMilli(),
		Tag: e.ID, URL: "/events/" + e.ID,
		Renotify:           m.Kind != routing.KindResolved,
		RequireInteraction: e.Level == levels.Critical && m.Kind != routing.KindResolved,
	}
	if m.AlertID != "" {
		p.AlertID, p.Tag, p.URL = m.AlertID, m.AlertID, "/alerts/"+m.AlertID
		if ackToken != "" && m.Kind != routing.KindResolved {
			p.Ack = ackToken
			p.Actions = append(p.Actions, WebAction{Action: "ack", Title: "Acknowledge"})
		}
	}
	if m.Kind != routing.KindResolved {
		for i, a := range e.Actions {
			p.Actions = append(p.Actions, WebAction{Action: "link" + string(rune('0'+i)), Title: a.Label, URL: a.URL})
		}
	}
	if len([]rune(p.Body)) > 600 {
		p.Body = string([]rune(p.Body)[:600]) + "…"
	}
	out, _ := json.Marshal(p)
	if len(out) > webpush.MaxPayload {
		var keep []WebAction
		for _, a := range p.Actions {
			if a.URL == "" {
				keep = append(keep, a)
			}
		}
		p.Actions = keep
		out, _ = json.Marshal(p)
	}
	if len(out) > webpush.MaxPayload {
		p.Body = string([]rune(p.Body)[:min(len([]rune(p.Body)), 120)]) + "…"
		out, _ = json.Marshal(p)
	}
	return out
}

func webOptions(m Message) webpush.Options {
	o := webpush.Options{TTL: 24 * time.Hour, Urgency: "normal", Topic: topic(m)}
	switch {
	case m.Kind == routing.KindResolved:
		o.TTL = 6 * time.Hour
	case m.Event.Level == levels.Critical || m.Event.Level == levels.Error:
		o.Urgency = "high"
	}
	return o
}

// topic lets a newer push for the same alert replace one still waiting at
// the push service (an offline phone then gets only the latest state).
func topic(m Message) string {
	t := m.AlertID
	if t == "" {
		t = m.Event.ID
	}
	if len(t) > 32 {
		t = t[:32]
	}
	return t
}

// sendWeb delivers m to one web-push device and fills in rec.
func (d *Dispatcher) sendWeb(ctx context.Context, dev devices.Device, m Message, rec *Delivery) {
	if d.web == nil {
		rec.Status, rec.Error = StatusSkipped, "Web Push is not configured"
		return
	}
	sub := webpush.Subscription{Endpoint: *dev.DeviceToken}
	if err := json.Unmarshal([]byte(dev.PushKeys), &sub.Keys); err != nil {
		rec.Status, rec.Error = StatusFailed, "stored subscription keys are unreadable"
		return
	}
	token := ""
	if m.AlertID != "" && d.sign != nil {
		token = d.sign(m.AlertID)
	}
	sctx, cancel := context.WithTimeout(ctx, d.timeout)
	loc, err := d.web.Send(sctx, sub, BuildWebPayload(m, token, time.Now()), webOptions(m))
	cancel()
	if err != nil {
		rec.Status, rec.Error = StatusFailed, err.Error()
		var we *webpush.Error
		if errors.As(err, &we) && we.Gone() {
			_ = d.devices.ClearToken(ctx, dev.ID)
		}
		d.log.Warn("push.failed", "event_id", m.Event.ID, "device_id", dev.ID, "channel", "web", "error", rec.Error)
		return
	}
	rec.Status, rec.APNSID = StatusSent, loc
	d.log.Info("push.sent", "event_id", m.Event.ID, "device_id", dev.ID, "channel", "web", "kind", m.Kind)
}
