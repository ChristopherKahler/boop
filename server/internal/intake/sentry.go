package intake

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Sentry webhooks from an Internal Integration (Settings → Custom
// Integrations). The Sentry-Hook-Resource header says what it is about:
//   - issue: created / unresolved open an alert, resolved resolves it
//   - event_alert: an issue alert rule fired (opens or repeats the alert for
//     that issue, so the issue's resolve webhook closes it)
//   - metric_alert: critical / warning open, resolved resolves
//
// With a signing secret (the integration's Client Secret) on the intake,
// Sentry-Hook-Signature must match. Apps can also report straight to Boop
// with a Sentry SDK DSN; that path is /api/{id}/envelope/, not this one.

type sentryIssue struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	Priority  string `json:"priority"`
	Permalink string `json:"permalink"`
	WebURL    string `json:"web_url"`
	Count     string `json:"count"`
	Project   struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"project"`
}

type sentryAlertEvent struct {
	EventID     string          `json:"event_id"`
	IssueID     string          `json:"issue_id"`
	Title       string          `json:"title"`
	Level       string          `json:"level"`
	Culprit     string          `json:"culprit"`
	WebURL      string          `json:"web_url"`
	Environment string          `json:"environment"`
	Release     json.RawMessage `json:"release"`
	Datetime    string          `json:"datetime"`
}

func sentryLevel(l, def string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "fatal":
		return levels.Critical
	case "error":
		return levels.Error
	case "warning":
		return levels.Warning
	case "info", "debug", "sample":
		return levels.Info
	}
	return def
}

func mapSentry(r Request) (Result, error) {
	if r.Secret != "" && !validHMAC(r.Secret, r.Body, r.Header.Get("Sentry-Hook-Signature"), "") {
		return Result{}, ErrSignature
	}
	var p struct {
		Action string          `json:"action"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return Result{}, ErrPayload
	}
	switch resource := r.Header.Get("Sentry-Hook-Resource"); resource {
	case "issue":
		var d struct {
			Issue sentryIssue `json:"issue"`
		}
		if err := json.Unmarshal(p.Data, &d); err != nil || d.Issue.ID == "" {
			return Result{}, ErrPayload
		}
		is := d.Issue
		in := events.Input{
			Source: "sentry", Type: "issue." + p.Action, ExternalID: is.ID, Fingerprint: "sentry:issue:" + is.ID,
			Title: is.Title, Body: joinNonEmpty(" · ", is.Project.Name, is.ShortID, is.Culprit),
			Data: data(map[string]any{"issue_id": is.ID, "short_id": is.ShortID, "project": is.Project.Slug, "priority": is.Priority, "count": is.Count}),
		}
		if u := firstNonEmpty(is.WebURL, is.Permalink); u != "" {
			in.Actions = []events.Action{{Label: "Open in Sentry", URL: u}}
		}
		switch p.Action {
		case "created", "unresolved":
			in.Level = sentryLevel(is.Level, levels.Error)
			return Result{Events: []events.Input{in}}, nil
		case "resolved":
			in.Resolve, in.Level = true, levels.Success
			in.Title = "Resolved in Sentry: " + is.Title
			return Result{Events: []events.Input{in}}, nil
		default:
			return ignored("Sentry issue " + p.Action)
		}
	case "event_alert", "error":
		var d struct {
			Event         sentryAlertEvent `json:"event"`
			Error         sentryAlertEvent `json:"error"`
			TriggeredRule string           `json:"triggered_rule"`
		}
		if err := json.Unmarshal(p.Data, &d); err != nil {
			return Result{}, ErrPayload
		}
		ev := d.Event
		if resource == "error" {
			ev = d.Error
		}
		if ev.IssueID == "" && ev.EventID == "" {
			return Result{}, ErrPayload
		}
		fp := "sentry:issue:" + ev.IssueID
		if ev.IssueID == "" {
			fp = "sentry:event:" + ev.EventID
		}
		in := events.Input{
			Source: "sentry", Type: resource, ExternalID: ev.EventID, Fingerprint: fp, OccurredAt: ev.Datetime,
			Title: ev.Title, Level: sentryLevel(ev.Level, levels.Error),
			Body: joinNonEmpty(" · ", ev.Culprit, prefixed("env ", ev.Environment), prefixed("rule: ", d.TriggeredRule)),
			Data: data(map[string]any{"event_id": ev.EventID, "issue_id": ev.IssueID, "rule": d.TriggeredRule, "environment": ev.Environment}),
		}
		if ev.WebURL != "" {
			in.Actions = []events.Action{{Label: "Open in Sentry", URL: ev.WebURL}}
		}
		return Result{Events: []events.Input{in}}, nil
	case "metric_alert":
		var d struct {
			DescriptionTitle string `json:"description_title"`
			DescriptionText  string `json:"description_text"`
			WebURL           string `json:"web_url"`
			MetricAlert      struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				AlertRule struct {
					ID   json.RawMessage `json:"id"`
					Name string          `json:"name"`
				} `json:"alert_rule"`
			} `json:"metric_alert"`
		}
		if err := json.Unmarshal(p.Data, &d); err != nil {
			return Result{}, ErrPayload
		}
		rule := strings.Trim(string(d.MetricAlert.AlertRule.ID), `"`)
		if rule == "" || rule == "null" {
			rule = d.MetricAlert.AlertRule.Name
		}
		in := events.Input{
			Source: "sentry", Type: "metric_alert." + p.Action, Fingerprint: "sentry:metric:" + rule,
			Title: firstNonEmpty(d.DescriptionTitle, d.MetricAlert.AlertRule.Name, d.MetricAlert.Title),
			Body:  strings.ReplaceAll(d.DescriptionText, `\n`, "\n"),
			Data:  data(map[string]any{"rule": d.MetricAlert.AlertRule.Name, "incident": d.MetricAlert.ID}),
		}
		if d.WebURL != "" {
			in.Actions = []events.Action{{Label: "Open in Sentry", URL: d.WebURL}}
		}
		switch p.Action {
		case "critical":
			in.Level = levels.Critical
		case "warning":
			in.Level = levels.Warning
		case "resolved":
			in.Resolve, in.Level = true, levels.Success
		default:
			return ignored("Sentry metric alert " + p.Action)
		}
		return Result{Events: []events.Input{in}}, nil
	case "installation":
		return ignored("Sentry installation " + p.Action + " (the integration is connected)")
	case "":
		return mapSentryLegacy(r.Body)
	default:
		return ignored(fmt.Sprintf("Sentry %s webhook (not used)", resource))
	}
}

// mapSentryLegacy handles the older per-project "WebHooks" plugin, which
// posts an issue alert with no Sentry-Hook-Resource header.
func mapSentryLegacy(body []byte) (Result, error) {
	var p struct {
		ID              string   `json:"id"`
		ProjectName     string   `json:"project_name"`
		Culprit         string   `json:"culprit"`
		Level           string   `json:"level"`
		URL             string   `json:"url"`
		Message         string   `json:"message"`
		TriggeringRules []string `json:"triggering_rules"`
		Event           struct {
			Title   string `json:"title"`
			EventID string `json:"event_id"`
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &p); err != nil || (p.ID == "" && p.Event.EventID == "") {
		return Result{}, ErrPayload
	}
	in := events.Input{
		Source: "sentry", Type: "legacy_alert", ExternalID: p.Event.EventID, Fingerprint: "sentry:issue:" + p.ID,
		Title: firstNonEmpty(p.Event.Title, p.Message), Level: sentryLevel(p.Level, levels.Error),
		Body: joinNonEmpty(" · ", p.ProjectName, p.Culprit, prefixed("rule: ", strings.Join(p.TriggeringRules, ", "))),
	}
	if p.URL != "" {
		in.Actions = []events.Action{{Label: "Open in Sentry", URL: p.URL}}
	}
	return Result{Events: []events.Input{in}}, nil
}

func prefixed(prefix, s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return prefix + s
}
