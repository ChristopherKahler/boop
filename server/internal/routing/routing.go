// Package routing decides whether an event pushes to the phone or only lands
// in the inbox. Routing is set per client (which levels push, whether
// recoveries push) and a project may override the levels.
package routing

import (
	"github.com/chrisgreg/boop/server/internal/alerts"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Kinds of push.
const (
	KindAlert     = "alert"     // a new alert opened
	KindEscalated = "escalated" // an open alert got more severe
	KindResolved  = "resolved"  // an alert that pushed has recovered
	KindEvent     = "event"     // an info/success event that is not an alert
)

// Input is everything the decision depends on.
type Input struct {
	Notify         bool     // the project's push switch
	PushLevels     []string // effective levels that push (project override or client)
	PushRecoveries bool     // the client's recovery switch
	Level          string   // the event's level
	Change         alerts.Change
	AlertLevel     string // the alert's level after this event (or before, for a resolve)
	Silenced       bool
}

// Decision is the outcome, with a short reason shown in the UI.
type Decision struct {
	Push   bool
	Kind   string
	Reason string
}

// Decide applies the routing rules:
//   - silenced events and projects with notifications off never push;
//   - a new alert pushes when its level is in the push levels;
//   - a repeat of an open alert never pushes (it is counted instead);
//   - an escalation pushes when the new level is in the push levels;
//   - a recovery pushes when the client wants recoveries and the alert's
//     level was one that pushes (you hear "resolved" for what you heard about);
//   - other events push when their level is in the push levels.
func Decide(in Input) Decision {
	if in.Silenced {
		return Decision{Reason: "inbox: silenced"}
	}
	if !in.Notify {
		return Decision{Reason: "inbox: project notifications are off"}
	}
	in2 := func(l string) bool { return levels.Contains(in.PushLevels, l) }
	switch in.Change {
	case alerts.Opened:
		if in2(in.Level) {
			return Decision{Push: true, Kind: KindAlert, Reason: "pushed: alert opened"}
		}
		return Decision{Reason: "inbox: " + in.Level + " does not push for this project"}
	case alerts.Repeated:
		return Decision{Reason: "inbox: repeat of an open alert"}
	case alerts.Escalated:
		if in2(in.AlertLevel) {
			return Decision{Push: true, Kind: KindEscalated, Reason: "pushed: alert escalated to " + in.AlertLevel}
		}
		return Decision{Reason: "inbox: escalated to " + in.AlertLevel + ", which does not push"}
	case alerts.Resolved:
		if !in.PushRecoveries {
			return Decision{Reason: "inbox: recovery pushes are off for this client"}
		}
		if in2(in.AlertLevel) {
			return Decision{Push: true, Kind: KindResolved, Reason: "pushed: alert resolved"}
		}
		return Decision{Reason: "inbox: resolved an alert that did not push"}
	default:
		if in2(in.Level) {
			return Decision{Push: true, Kind: KindEvent, Reason: "pushed"}
		}
		return Decision{Reason: "inbox: " + in.Level + " does not push for this project"}
	}
}
