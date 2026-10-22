package intake

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Laravel Nightwatch: the application's Issues settings → Webhooks (one per
// application). issue.opened and issue.reopened open an alert for that
// issue; issue.resolved and issue.ignored close it. With the webhook's
// signing secret on the intake, Nightwatch-Signature (HMAC-SHA256 of the
// raw body) must match.

func mapNightwatch(r Request) (Result, error) {
	if r.Secret != "" && !validHMAC(r.Secret, r.Body, r.Header.Get("Nightwatch-Signature"), "") {
		return Result{}, ErrSignature
	}
	var p struct {
		Event     string `json:"event"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Issue struct {
				ID       string `json:"id"`
				Ref      int    `json:"ref"`
				Type     string `json:"type"`
				Title    string `json:"title"`
				Priority string `json:"priority"`
				URL      string `json:"url"`
				Details  struct {
					Type      string   `json:"type"`
					Handled   bool     `json:"handled"`
					Class     string   `json:"class"`
					Message   string   `json:"message"`
					File      string   `json:"file"`
					Line      int      `json:"line"`
					Methods   []string `json:"methods"`
					Path      string   `json:"path"`
					Name      string   `json:"name"`
					Duration  string   `json:"duration"`
					Threshold string   `json:"threshold"`
				} `json:"details"`
			} `json:"issue"`
			Environment struct {
				Name string `json:"name"`
			} `json:"environment"`
			Actor *struct {
				Name string `json:"name"`
			} `json:"actor"`
			ApplicationID string `json:"application_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil || p.Payload.Issue.ID == "" {
		return Result{}, ErrPayload
	}
	is, d := p.Payload.Issue, p.Payload.Issue.Details
	where := ""
	switch d.Type {
	case "exception":
		where = joinNonEmpty(" ", d.Class)
		if d.File != "" {
			where = joinNonEmpty(" · ", where, d.File+":"+strconv.Itoa(d.Line))
		}
	case "slow-route":
		where = joinNonEmpty(" ", strings.Join(d.Methods, "|"), d.Path)
	default:
		where = d.Name
	}
	slow := ""
	if d.Duration != "" {
		slow = d.Duration + " (threshold " + d.Threshold + ")"
	}
	in := events.Input{
		Source: "nightwatch", Type: strings.TrimPrefix(p.Event, "issue."), OccurredAt: p.Timestamp,
		Fingerprint: "nightwatch:" + is.ID, ExternalID: is.ID, Title: is.Title,
		Body: joinNonEmpty(" · ", prefixed("#", refString(is.Ref)), p.Payload.Environment.Name, where, slow),
		Data: data(map[string]any{"issue": is.ID, "ref": is.Ref, "type": is.Type, "priority": is.Priority, "environment": p.Payload.Environment.Name,
			"handled": d.Handled, "message": clip(d.Message, 2000)}),
	}
	if is.URL != "" {
		in.Actions = []events.Action{{Label: "Open in Nightwatch", URL: is.URL}}
	}
	switch p.Event {
	case "issue.opened", "issue.reopened":
		in.Level = nightwatchLevel(is.Type, is.Priority)
	case "issue.resolved", "issue.ignored":
		in.Resolve, in.Level = true, levels.Success
		verb := "Resolved"
		if p.Event == "issue.ignored" {
			verb = "Ignored"
		}
		in.Title = verb + " in Nightwatch: " + is.Title
		if p.Payload.Actor != nil && p.Payload.Actor.Name != "" {
			in.Body = joinNonEmpty(" · ", in.Body, "by "+p.Payload.Actor.Name)
		}
	default:
		return ignored("Nightwatch " + p.Event)
	}
	return Result{Events: []events.Input{in}}, nil
}

// nightwatchLevel: high-priority issues are critical, exceptions are errors,
// slow routes and jobs are warnings unless marked medium or high.
func nightwatchLevel(typ, priority string) string {
	switch priority {
	case "high":
		return levels.Critical
	case "medium":
		return levels.Error
	}
	if typ == "exception" {
		return levels.Error
	}
	return levels.Warning
}

func refString(ref int) string {
	if ref <= 0 {
		return ""
	}
	return strconv.Itoa(ref)
}
