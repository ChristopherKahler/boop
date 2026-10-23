package api

import (
	"context"

	"github.com/chrisgreg/boop/server/internal/alerts"
	"github.com/chrisgreg/boop/server/internal/delivery"
	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/projects"
	"github.com/chrisgreg/boop/server/internal/routing"
)

// ingested is the outcome of running one event through the pipeline.
type ingested struct {
	Event    events.Event
	Alert    *alerts.Alert
	Change   alerts.Change
	Decision routing.Decision
	// Message is set when the decision is to push.
	Message *delivery.Message
}

// ingest is the one path every incoming event takes: store (with redaction),
// silences, alert grouping, then routing. It does not deliver; callers either
// Enqueue the message (ingestion) or Deliver it synchronously (test button,
// unsilence). via names the intake for the log ("api", "sentry", ...).
func (s *Server) ingest(ctx context.Context, p projects.Project, in events.Input, via string) (ingested, error) {
	e, err := s.Events.Create(ctx, p.ID, in, s.redactor(ctx))
	if err != nil {
		return ingested{}, err
	}
	s.Log.Info("event.created", "event_id", e.ID, "project_id", p.ID, "event_level", e.Level, "via", via)
	silenced := s.applySilence(ctx, &e)
	att, err := s.Alerts.Attach(ctx, e, in.Resolve)
	if err != nil {
		// The event is stored; losing its alert link is better than losing it.
		s.Log.Error("alert.attach_failed", "event_id", e.ID, "error", err.Error())
	}
	if att.Alert != nil {
		e.AlertID = att.Alert.ID
		s.Log.Info("alert."+string(att.Change), "alert_id", att.Alert.ID, "event_id", e.ID)
	}
	return s.route(ctx, p, e, att, silenced), nil
}

// route decides whether e pushes and records the reason on the event.
func (s *Server) route(ctx context.Context, p projects.Project, e events.Event, att alerts.Attachment, silenced bool) ingested {
	out := ingested{Event: e, Alert: att.Alert, Change: att.Change}
	c, err := s.Clients.Get(ctx, p.ClientID)
	if err != nil {
		s.Log.Error("routing.client_lookup_failed", "project_id", p.ID, "error", err.Error())
	}
	in := routing.Input{
		Notify: p.Notify, PushLevels: p.EffectivePushLevels, PushRecoveries: c.PushRecoveries,
		Level: e.Level, Change: att.Change, Silenced: silenced,
	}
	if att.Alert != nil {
		in.AlertLevel = att.Alert.Level
	}
	out.Decision = routing.Decide(in)
	out.Event.Route = out.Decision.Reason
	if err := s.Events.SetRoute(ctx, e.ID, out.Decision.Reason); err != nil {
		s.Log.Error("routing.record_failed", "event_id", e.ID, "error", err.Error())
	}
	if out.Decision.Push {
		m := delivery.Message{Event: out.Event, Project: p, ClientName: c.Name, Kind: out.Decision.Kind}
		if att.Alert != nil {
			m.AlertID, m.AlertTitle = att.Alert.ID, att.Alert.Title
		}
		out.Message = &m
	}
	return out
}

// ingestAndEnqueue runs ingest and queues the push, if any.
func (s *Server) ingestAndEnqueue(ctx context.Context, p projects.Project, in events.Input, via string) (ingested, error) {
	res, err := s.ingest(ctx, p, in, via)
	if err == nil && res.Message != nil {
		s.Dispatcher.Enqueue(*res.Message)
	}
	return res, err
}
