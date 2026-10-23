package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hookSample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "intake", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// post sends a raw webhook to url with headers (name, value pairs).
func (e *env) post(url string, body []byte, headers ...string) resp {
	e.t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, raw: raw}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

func TestHooksEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.do("POST", "/api/v1/clients", "", map[string]any{"name": "Acme Motors"})
	e.do("POST", "/api/v1/clients", "", map[string]any{"name": "Riverbend Dental"})
	site := e.do("POST", "/api/v1/projects", "", map[string]any{"name": "Dealer site", "client_id": "acme-motors"}).body["id"].(string)
	app := e.do("POST", "/api/v1/projects", "", map[string]any{"name": "Booking app", "client_id": "riverbend-dental"}).body["id"].(string)

	newIntake := func(project, kind string, extra map[string]any) (url, token string) {
		t.Helper()
		body := map[string]any{"kind": kind}
		for k, v := range extra {
			body[k] = v
		}
		r := e.do("POST", "/api/v1/projects/"+project+"/intakes", "", body)
		if r.status != 201 {
			t.Fatalf("create %s intake: %d %s", kind, r.status, r.raw)
		}
		token = r.body["token"].(string)
		if !strings.HasSuffix(r.body["url"].(string), "/hooks/"+kind+"/"+token) || r.body["token_hint"] != token[len(token)-4:] {
			t.Errorf("intake url: %s", r.raw)
		}
		return e.srv.URL + "/hooks/" + kind + "/" + token, token
	}
	kuma, _ := newIntake(site, "uptime-kuma", nil)
	hc, _ := newIntake(site, "healthchecks", map[string]any{"name": "Nightly jobs"})
	gh, ghToken := newIntake(site, "github", nil)
	sentry, _ := newIntake(app, "sentry", map[string]any{"signing_secret": "sentry-client-secret"})
	nw, _ := newIntake(app, "nightwatch", nil)
	var mapping map[string]any
	_ = json.Unmarshal(hookSample(t, "generic-mapping.json"), &mapping)
	graf, _ := newIntake(app, "generic", map[string]any{"name": "Grafana", "mapping": mapping})

	sig := func(secret string, body []byte) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write(body)
		return hex.EncodeToString(m.Sum(nil))
	}
	expect := func(r resp, n int) []any {
		t.Helper()
		if r.status != 200 {
			t.Fatalf("hook: %d %s", r.status, r.raw)
		}
		evs := r.body["events"].([]any)
		if len(evs) != n {
			t.Fatalf("hook stored %d events, want %d: %s", len(evs), n, r.raw)
		}
		return evs
	}

	// Problems from every service open alerts under the right client.
	expect(e.post(kuma, hookSample(t, "kuma-down.json")), 1)
	expect(e.post(hc, hookSample(t, "healthchecks-down.json")), 1)
	ghBody := hookSample(t, "github-failure.json")
	expect(e.post(gh, ghBody, "X-GitHub-Event", "workflow_run", "X-Hub-Signature-256", "sha256="+sig(ghToken, ghBody)), 1)
	sBody := hookSample(t, "sentry-issue-created.json")
	expect(e.post(sentry, sBody, "Sentry-Hook-Resource", "issue", "Sentry-Hook-Signature", sig("sentry-client-secret", sBody)), 1)
	aBody := hookSample(t, "sentry-event-alert.json")
	alertEv := expect(e.post(sentry, aBody, "Sentry-Hook-Resource", "event_alert", "Sentry-Hook-Signature", sig("sentry-client-secret", aBody)), 1)
	if alertEv[0].(map[string]any)["alert"] != "repeated" {
		t.Errorf("Sentry alert rule should repeat the issue's alert: %v", alertEv)
	}
	expect(e.post(nw, hookSample(t, "nightwatch-opened.json")), 1)
	expect(e.post(graf, hookSample(t, "generic-batch.json")), 2) // Watchdog ignored, one resolve with nothing open

	r := e.do("GET", "/api/v1/alerts?status=open&client=acme-motors", "", nil)
	acme := r.body["alerts"].([]any)
	r = e.do("GET", "/api/v1/alerts?status=open&client=riverbend-dental", "", nil)
	river := r.body["alerts"].([]any)
	titles := func(list []any) string {
		var out []string
		for _, a := range list {
			out = append(out, a.(map[string]any)["source"].(string)+"|"+a.(map[string]any)["title"].(string))
		}
		return strings.Join(out, "\n")
	}
	if len(acme) != 3 || len(river) != 3 {
		t.Fatalf("open alerts: acme %d, riverbend %d\n%s\n--\n%s", len(acme), len(river), titles(acme), titles(river))
	}
	for _, want := range []string{"uptime-kuma|Dealer website is down", "healthchecks|Nightly inventory sync missed its check-in", "github-actions|Deploy failed on main"} {
		if !strings.Contains(titles(acme), want) {
			t.Errorf("acme alerts missing %q:\n%s", want, titles(acme))
		}
	}
	for _, want := range []string{"sentry|TypeError", "nightwatch|Illuminate", "grafana|DiskAlmostFull"} {
		if !strings.Contains(titles(river), want) {
			t.Errorf("riverbend alerts missing %q:\n%s", want, titles(river))
		}
	}

	// Recoveries from every service resolve their alert.
	expect(e.post(kuma, hookSample(t, "kuma-up.json")), 1)
	expect(e.post(hc, hookSample(t, "healthchecks-up.json")), 1)
	okBody := hookSample(t, "github-success.json")
	expect(e.post(gh, okBody, "X-GitHub-Event", "workflow_run", "X-Hub-Signature-256", "sha256="+sig(ghToken, okBody)), 1)
	rBody := hookSample(t, "sentry-issue-resolved.json")
	expect(e.post(sentry, rBody, "Sentry-Hook-Resource", "issue", "Sentry-Hook-Signature", sig("sentry-client-secret", rBody)), 1)
	expect(e.post(nw, hookSample(t, "nightwatch-resolved.json")), 1)
	if r := e.do("GET", "/api/v1/alerts?status=active&client=acme-motors", "", nil); len(r.body["alerts"].([]any)) != 0 {
		t.Errorf("acme alerts still open after recoveries:\n%s", titles(r.body["alerts"].([]any)))
	}
	r = e.do("GET", "/api/v1/alerts?status=active&client=riverbend-dental", "", nil)
	if left := r.body["alerts"].([]any); len(left) != 1 || !strings.Contains(titles(left), "DiskAlmostFull") {
		t.Errorf("riverbend should only have the disk alert open:\n%s", titles(left))
	}
	r = e.do("GET", "/api/v1/alerts?status=resolved", "", nil)
	for _, a := range r.body["alerts"].([]any) {
		if a.(map[string]any)["resolved_by"] != "recovery" {
			t.Errorf("resolved by %v", a.(map[string]any)["resolved_by"])
		}
	}

	// Ignored and rejected deliveries.
	if r := e.post(gh, []byte(`{"zen":"hi"}`), "X-GitHub-Event", "ping"); r.status != 200 || !strings.Contains(r.body["ignored"].(string), "ping") {
		t.Errorf("github ping: %d %s", r.status, r.raw)
	}
	if r := e.post(sentry, sBody, "Sentry-Hook-Resource", "issue", "Sentry-Hook-Signature", sig("wrong", sBody)); r.status != 401 {
		t.Errorf("bad sentry signature: %d", r.status)
	}
	if r := e.post(kuma, []byte("not json")); r.status != 400 {
		t.Errorf("garbage: %d", r.status)
	}
	if r := e.post(e.srv.URL+"/hooks/uptime-kuma/boop_hook_nope", hookSample(t, "kuma-down.json")); r.status != 404 {
		t.Errorf("unknown token: %d", r.status)
	}
	// A token only works with its own kind.
	if r := e.post(strings.Replace(kuma, "/uptime-kuma/", "/healthchecks/", 1), hookSample(t, "healthchecks-down.json")); r.status != 404 {
		t.Errorf("token used with another kind: %d", r.status)
	}

	// The intake list shows what arrived.
	r = e.do("GET", "/api/v1/intakes?project="+site, "", nil)
	for _, it := range r.body["intakes"].([]any) {
		m := it.(map[string]any)
		if m["received_count"].(float64) < 1 || m["last_result"] == "" || m["last_received_at"] == nil {
			t.Errorf("intake bookkeeping: %v", m)
		}
		if _, leaked := m["token"]; leaked {
			t.Errorf("token returned in list")
		}
	}
	if !strings.Contains(string(r.raw), `"signing_secret_set":false`) || strings.Contains(string(e.do("GET", "/api/v1/intakes", "", nil).raw), "sentry-client-secret") {
		t.Errorf("signing secret leaked or flag missing")
	}
}

func TestIntakeManagementAndPreview(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.createProject("Site")
	if r := e.do("POST", "/api/v1/projects/"+pid+"/intakes", "", map[string]any{"kind": "pagerduty"}); r.status != 422 {
		t.Errorf("unknown kind: %d", r.status)
	}
	if r := e.do("POST", "/api/v1/projects/"+pid+"/intakes", "", map[string]any{"kind": "generic"}); r.status != 422 {
		t.Errorf("generic without mapping: %d", r.status)
	}
	if r := e.do("POST", "/api/v1/projects/prj_nope/intakes", "", map[string]any{"kind": "github"}); r.status != 404 {
		t.Errorf("unknown project: %d", r.status)
	}
	r := e.do("POST", "/api/v1/projects/"+pid+"/intakes", "", map[string]any{"kind": "uptime-kuma"})
	id, old := r.body["id"].(string), r.body["url"].(string)
	r = e.do("POST", "/api/v1/intakes/"+id+"/rotate", "", nil)
	if r.status != 200 || r.body["url"] == old {
		t.Fatalf("rotate: %s", r.raw)
	}
	oldURL := strings.Replace(old, "http://"+strings.TrimPrefix(e.srv.URL, "http://"), e.srv.URL, 1)
	if r := e.post(oldURL, hookSample(t, "kuma-down.json")); r.status != 404 {
		t.Errorf("old address still works after rotate: %d", r.status)
	}
	r = e.do("PATCH", "/api/v1/intakes/"+id, "", map[string]any{"name": "Status page"})
	if r.status != 200 || r.body["name"] != "Status page" {
		t.Errorf("rename: %s", r.raw)
	}

	// Preview a generic mapping against a sample payload without storing anything.
	var mapping, payload json.RawMessage = hookSample(t, "generic-mapping.json"), hookSample(t, "generic-batch.json")
	r = e.do("POST", "/api/v1/intakes/preview", "", map[string]any{"kind": "generic", "mapping": mapping, "payload": payload})
	if r.status != 200 || len(r.body["events"].([]any)) != 2 {
		t.Fatalf("preview: %d %s", r.status, r.raw)
	}
	if r := e.do("POST", "/api/v1/intakes/preview", "", map[string]any{"kind": "generic", "mapping": map[string]any{"body": "x"}, "payload": payload}); r.status != 422 {
		t.Errorf("preview with a bad mapping: %d", r.status)
	}
	if n := e.do("GET", "/api/v1/events", "", nil).body["events"].([]any); len(n) != 0 {
		t.Errorf("preview stored %d events", len(n))
	}
	if r := e.do("DELETE", "/api/v1/intakes/"+id, "", nil); r.status != 204 {
		t.Errorf("delete: %d", r.status)
	}
}
