package api

import (
	"context"
	"testing"
	"time"
)

func TestClientsAndProjectRouting(t *testing.T) {
	e := newEnv(t)

	r := e.do("GET", "/api/v1/clients", "", nil)
	if r.status != 200 || len(r.body["clients"].([]any)) != 1 {
		t.Fatalf("default client: %d %s", r.status, r.raw)
	}

	r = e.do("POST", "/api/v1/clients", "", map[string]any{"name": "Acme Motors"})
	if r.status != 201 || r.body["slug"] != "acme-motors" || r.body["push_recoveries"] != true || join(r.body["push_levels"]) != "warning,error,critical" {
		t.Fatalf("create client: %d %s", r.status, r.raw)
	}
	acme := r.body["id"].(string)
	if r := e.do("POST", "/api/v1/clients", "", map[string]any{"name": " "}); r.status != 422 {
		t.Errorf("blank client name: %d", r.status)
	}
	if r := e.do("POST", "/api/v1/clients", "", map[string]any{"name": "X", "push_levels": []string{"fatal"}}); r.status != 422 {
		t.Errorf("bad push level: %d", r.status)
	}

	// A project joins a client by slug and inherits its routing.
	r = e.do("POST", "/api/v1/projects", "", map[string]any{"name": "Dealer site", "client_id": "acme-motors"})
	if r.status != 201 || r.body["client_id"] != acme || r.body["client_name"] != "Acme Motors" || r.body["push_levels"] != nil ||
		join(r.body["effective_push_levels"]) != "warning,error,critical" || r.body["min_level"] != "warning" {
		t.Fatalf("create project under client: %d %s", r.status, r.raw)
	}
	pid := r.body["id"].(string)

	// Override, then go back to the client's routing.
	r = e.do("PATCH", "/api/v1/projects/"+pid, "", map[string]any{"push_levels": []string{"critical"}})
	if r.status != 200 || join(r.body["push_levels"]) != "critical" || join(r.body["effective_push_levels"]) != "critical" {
		t.Fatalf("override: %d %s", r.status, r.raw)
	}
	r = e.do("PATCH", "/api/v1/projects/"+pid, "", map[string]any{"push_levels": nil})
	if r.status != 200 || r.body["push_levels"] != nil || join(r.body["effective_push_levels"]) != "warning,error,critical" {
		t.Fatalf("inherit again: %d %s", r.status, r.raw)
	}
	// Client routing changes flow to inheriting projects.
	e.do("PATCH", "/api/v1/clients/"+acme, "", map[string]any{"push_levels": []string{"error", "critical"}})
	r = e.do("GET", "/api/v1/projects/"+pid, "", nil)
	if join(r.body["effective_push_levels"]) != "error,critical" {
		t.Errorf("inherited routing: %s", r.raw)
	}
	if r := e.do("PATCH", "/api/v1/projects/"+pid, "", map[string]any{"client_id": "nope"}); r.status != 422 {
		t.Errorf("unknown client: %d %s", r.status, r.raw)
	}

	// A client with projects cannot be deleted; an empty one can.
	if r := e.do("DELETE", "/api/v1/clients/"+acme, "", nil); r.status != 409 {
		t.Errorf("delete client in use: %d %s", r.status, r.raw)
	}
	e.do("PATCH", "/api/v1/projects/"+pid, "", map[string]any{"client_id": "internal"})
	if r := e.do("DELETE", "/api/v1/clients/"+acme, "", nil); r.status != 204 {
		t.Errorf("delete empty client: %d %s", r.status, r.raw)
	}
}

func TestAlertLifecycle(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/clients", "", map[string]any{"name": "Acme"})
	r := e.do("POST", "/api/v1/projects", "", map[string]any{"name": "API", "client_id": "acme"})
	key := r.body["api_key"].(string)
	_, cred := e.pairDevice("phone")
	e.do("POST", "/api/v1/devices", cred, map[string]string{"device_token": "tok"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.server.Dispatcher.Start(ctx)

	post := func(body map[string]any) map[string]any {
		t.Helper()
		r := e.do("POST", "/api/v1/events", key, body)
		if r.status != 201 {
			t.Fatalf("post %v: %d %s", body, r.status, r.raw)
		}
		return r.body
	}
	waitSent := func(n int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(e.sender.sent) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond) // and nothing more arrives
		if len(e.sender.sent) != n {
			t.Fatalf("pushes = %d, want %d", len(e.sender.sent), n)
		}
	}

	// First occurrence opens an alert and pushes.
	ev := post(map[string]any{"title": "DB down", "level": "error", "fingerprint": "db"})
	id, _ := ev["alert_id"].(string)
	if ev["alert"] != "opened" || ev["pushed"] != true || id == "" {
		t.Fatalf("open: %v", ev)
	}
	waitSent(1)
	if n := e.sender.sent[0].n; n.Title != "Acme · DB down" || n.Body != "API" {
		t.Errorf("push = %+v", n)
	}

	// A repeat is counted, not pushed.
	ev = post(map[string]any{"title": "DB down", "level": "error", "fingerprint": "db"})
	if ev["alert"] != "repeated" || ev["pushed"] != false || ev["alert_id"] != id || ev["route"] != "inbox: repeat of an open alert" {
		t.Fatalf("repeat: %v", ev)
	}
	// Getting worse pushes again.
	ev = post(map[string]any{"title": "DB down", "level": "critical", "fingerprint": "db"})
	if ev["alert"] != "escalated" || ev["pushed"] != true {
		t.Fatalf("escalate: %v", ev)
	}
	waitSent(2)
	if n := e.sender.sent[1].n; n.Title != "Acme · Now critical: DB down" || !n.Prominent {
		t.Errorf("escalation push = %+v", n)
	}
	r = e.do("GET", "/api/v1/alerts?status=open", "", nil)
	list := r.body["alerts"].([]any)
	if len(list) != 1 || r.body["open"] != float64(1) {
		t.Fatalf("open alerts: %s", r.raw)
	}
	a := list[0].(map[string]any)
	if a["count"] != float64(3) || a["level"] != "critical" || a["client_name"] != "Acme" || a["notified_at"] == nil {
		t.Errorf("alert = %v", a)
	}

	// Acknowledge from the web UI, then from a device (the first time sticks).
	r = e.do("POST", "/api/v1/alerts/"+id+"/acknowledge", "", nil)
	if r.status != 200 || r.body["status"] != "acknowledged" || r.body["acknowledged_by"] != "web" || r.body["acknowledged_at"] == nil {
		t.Fatalf("ack: %d %s", r.status, r.raw)
	}
	ackedAt := r.body["acknowledged_at"]
	r = e.do("POST", "/api/v1/alerts/"+id+"/acknowledge", cred, nil)
	if r.status != 200 || r.body["acknowledged_at"] != ackedAt || r.body["acknowledged_by"] != "web" {
		t.Errorf("second ack changed it: %s", r.raw)
	}
	if r := e.do("GET", "/api/v1/alerts?status=open", "", nil); len(r.body["alerts"].([]any)) != 0 || r.body["acknowledged"] != float64(1) {
		t.Errorf("acknowledged alert still listed as open: %s", r.raw)
	}

	// A recovery event resolves it and pushes "resolved".
	ev = post(map[string]any{"title": "DB up", "fingerprint": "db", "resolve": true})
	if ev["alert"] != "resolved" || ev["alert_id"] != id || ev["pushed"] != true {
		t.Fatalf("recovery: %v", ev)
	}
	waitSent(3)
	if n := e.sender.sent[2].n; n.Title != "Acme · Resolved" || n.Body != "API: DB down" || n.Prominent {
		t.Errorf("resolved push = %+v", n)
	}
	r = e.do("GET", "/api/v1/alerts/"+id, "", nil)
	if r.body["status"] != "resolved" || r.body["resolved_by"] != "recovery" || r.body["resolve_event_id"] != ev["id"] || r.body["count"] != float64(3) {
		t.Errorf("resolved alert: %s", r.raw)
	}
	r = e.do("GET", "/api/v1/events?alert="+id, "", nil)
	if n := len(r.body["events"].([]any)); n != 4 {
		t.Errorf("alert occurrences (3 problems + recovery) = %d", n)
	}
	r = e.do("GET", "/api/v1/events/"+ev["id"].(string), "", nil)
	if r.body["level"] != "success" || r.body["alert_id"] != id {
		t.Errorf("recovery event: %s", r.raw)
	}

	// The same problem again is a new incident.
	ev = post(map[string]any{"title": "DB down", "level": "error", "fingerprint": "db"})
	second, _ := ev["alert_id"].(string)
	if ev["alert"] != "opened" || second == id || ev["pushed"] != true {
		t.Fatalf("reoccurrence: %v", ev)
	}
	waitSent(4)
	// Reopening the old one would give the problem two open alerts.
	if r := e.do("POST", "/api/v1/alerts/"+id+"/reopen", "", nil); r.status != 409 {
		t.Errorf("reopen with a newer open alert: %d %s", r.status, r.raw)
	}
	r = e.do("POST", "/api/v1/alerts/"+second+"/resolve", cred, nil)
	if r.status != 200 || r.body["resolved_by"] != "device: phone" {
		t.Fatalf("resolve by device: %d %s", r.status, r.raw)
	}
	r = e.do("POST", "/api/v1/alerts/"+second+"/reopen", "", nil)
	if r.status != 200 || r.body["status"] != "open" || r.body["resolved_at"] != nil {
		t.Errorf("reopen: %d %s", r.status, r.raw)
	}

	// Info stays in the inbox (Acme pushes warning and above) and is no alert.
	ev = post(map[string]any{"title": "Deploy finished", "level": "info"})
	if ev["pushed"] != false || ev["alert_id"] != nil || ev["route"] != "inbox: info does not push for this project" {
		t.Errorf("info: %v", ev)
	}
	// A recovery with nothing open is just an event.
	ev = post(map[string]any{"title": "Cache up", "fingerprint": "cache", "resolve": true})
	if ev["alert_id"] != nil || ev["pushed"] != false {
		t.Errorf("orphan recovery: %v", ev)
	}
	// Without a fingerprint every problem is its own alert.
	a1 := post(map[string]any{"title": "Boom", "level": "warning"})["alert_id"]
	a2 := post(map[string]any{"title": "Boom", "level": "warning"})["alert_id"]
	if a1 == nil || a1 == a2 {
		t.Errorf("unfingerprinted alerts: %v %v", a1, a2)
	}
	waitSent(6)

	if r := e.do("GET", "/api/v1/alerts?status=bogus", "", nil); r.status != 422 {
		t.Errorf("bad status filter: %d", r.status)
	}
	if r := e.do("POST", "/api/v1/alerts/alr_nope/acknowledge", "", nil); r.status != 404 {
		t.Errorf("unknown alert: %d", r.status)
	}
	r = e.do("GET", "/api/v1/alerts?client=acme&status=active", "", nil)
	if n := len(r.body["alerts"].([]any)); n != 3 {
		t.Errorf("active alerts for acme = %d: %s", n, r.raw)
	}
}

func TestSilencedProblemsStillGroup(t *testing.T) {
	e := newEnv(t)
	pid, key := e.createProject("Uini")
	e.do("POST", "/api/v1/silences", "", map[string]string{"field": "fingerprint", "value": "flaky", "project_id": pid})
	first := e.do("POST", "/api/v1/events", key, map[string]any{"title": "Flaky", "level": "error", "fingerprint": "flaky"}).body
	second := e.do("POST", "/api/v1/events", key, map[string]any{"title": "Flaky", "level": "error", "fingerprint": "flaky"}).body
	if first["alert_id"] == nil || first["alert_id"] != second["alert_id"] || first["pushed"] != false || first["route"] != "inbox: silenced" {
		t.Fatalf("silenced grouping: %v %v", first, second)
	}
	r := e.do("GET", "/api/v1/alerts/"+first["alert_id"].(string), "", nil)
	if r.body["count"] != float64(2) || r.body["notified_at"] != nil {
		t.Errorf("silenced alert: %s", r.raw)
	}
}

func join(v any) string {
	list, _ := v.([]any)
	out := ""
	for i, x := range list {
		if i > 0 {
			out += ","
		}
		out += x.(string)
	}
	return out
}
