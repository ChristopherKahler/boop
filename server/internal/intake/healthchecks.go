package intake

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/chrisgreg/boop/server/internal/events"
	"github.com/chrisgreg/boop/server/internal/events/levels"
)

// Healthchecks (healthchecks.io or self-hosted) watches scheduled jobs and
// fires when a check goes down (a run was missed, ran late past its grace
// time, or reported failure) and when it comes back up. Its Webhook
// integration lets you set the URL and body per status; the recommended
// body for both is:
//
//	{"status": "$STATUS", "now": "$NOW", "check": $JSON}
//
// $JSON is the check as the Healthchecks API returns it. A flat body
// ({"status": "$STATUS", "name": "$NAME", "code": "$CODE"}), a form body or
// query parameters with the same names also work.

type hcCheck struct {
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Tags      string `json:"tags"`
	Desc      string `json:"desc"`
	Status    string `json:"status"`
	LastPing  string `json:"last_ping"`
	NextPing  string `json:"next_ping"`
	UniqueKey string `json:"unique_key"`
	UUID      string `json:"uuid"`
	PingURL   string `json:"ping_url"`
	Grace     int    `json:"grace"`
}

func mapHealthchecks(r Request) (Result, error) {
	var p struct {
		Status     string  `json:"status"`
		Now        string  `json:"now"`
		Name       string  `json:"name"`
		Code       string  `json:"code"`
		Slug       string  `json:"slug"`
		Tags       string  `json:"tags"`
		ExitStatus string  `json:"exitstatus"`
		URL        string  `json:"url"`
		Check      hcCheck `json:"check"`
	}
	body := strings.TrimSpace(string(r.Body))
	switch {
	case strings.HasPrefix(body, "{"):
		if err := json.Unmarshal(r.Body, &p); err != nil {
			return Result{}, ErrPayload
		}
	default:
		q := r.Query
		if body != "" {
			if form, err := url.ParseQuery(body); err == nil {
				q = form
			}
		}
		p.Status, p.Name, p.Code, p.Slug, p.Tags, p.Now, p.ExitStatus = q.Get("status"), q.Get("name"), q.Get("code"), q.Get("slug"), q.Get("tags"), q.Get("now"), q.Get("exitstatus")
	}
	c := p.Check
	name := firstNonEmpty(c.Name, p.Name, c.Slug, p.Slug, "Scheduled check")
	id := firstNonEmpty(c.UUID, p.Code, c.UniqueKey, c.Slug, p.Slug, name)
	in := events.Input{
		Source: "healthchecks", Fingerprint: "healthchecks:" + id, OccurredAt: p.Now,
		Data: data(map[string]any{"check": name, "tags": firstNonEmpty(c.Tags, p.Tags), "last_ping": c.LastPing, "next_ping": c.NextPing, "grace_seconds": c.Grace}),
	}
	if p.URL != "" {
		in.Actions = []events.Action{{Label: "Open check", URL: p.URL}}
	}
	switch strings.ToLower(strings.TrimSpace(p.Status)) {
	case "down":
		in.Type, in.Level = "down", levels.Error
		in.Title = name + " missed its check-in"
		failed := p.ExitStatus != "" && p.ExitStatus != "0" && p.ExitStatus != "-1"
		if failed {
			in.Title = name + " failed (exit status " + p.ExitStatus + ")"
		}
		in.Body = joinNonEmpty(" · ", c.Desc, prefixed("last ping ", c.LastPing), prefixed("tags ", firstNonEmpty(c.Tags, p.Tags)))
	case "up":
		in.Type, in.Level, in.Resolve = "up", levels.Success, true
		in.Title = name + " is back up"
	default:
		return ignored("Healthchecks status " + p.Status)
	}
	return Result{Events: []events.Input{in}}, nil
}
