package intake

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisgreg/boop/server/internal/events"
)

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func req(kind string, body []byte, headers ...string) Request {
	h := http.Header{}
	for i := 0; i+1 < len(headers); i += 2 {
		h.Set(headers[i], headers[i+1])
	}
	return Request{Kind: kind, Method: "POST", Header: h, Query: url.Values{}, Body: body}
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// one maps r and expects exactly one event.
func one(t *testing.T, r Request) events.Input {
	t.Helper()
	res, err := Map(r)
	if err != nil {
		t.Fatalf("%s: %v", r.Kind, err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("%s: %d events (ignored %q)", r.Kind, len(res.Events), res.Ignored)
	}
	return res.Events[0]
}

func TestSentry(t *testing.T) {
	in := one(t, req(KindSentry, sample(t, "sentry-issue-created.json"), "Sentry-Hook-Resource", "issue"))
	if in.Level != "error" || in.Resolve || in.Fingerprint != "sentry:issue:1234567890" || !strings.HasPrefix(in.Title, "TypeError") ||
		in.Body != "booking-app · BOOKING-Y · BookingForm.submit" || in.Actions[0].URL != "https://example-org.sentry.io/issues/1234567890/" {
		t.Errorf("issue created = %+v", in)
	}
	res := one(t, req(KindSentry, sample(t, "sentry-issue-resolved.json"), "Sentry-Hook-Resource", "issue"))
	if !res.Resolve || res.Fingerprint != in.Fingerprint || res.Level != "success" {
		t.Errorf("issue resolved = %+v", res)
	}
	// An alert rule firing joins the same issue's alert, so the issue's resolve closes it.
	al := one(t, req(KindSentry, sample(t, "sentry-event-alert.json"), "Sentry-Hook-Resource", "event_alert"))
	if al.Fingerprint != in.Fingerprint || al.Level != "error" || !strings.Contains(al.Body, "rule: Errors in production") || al.OccurredAt == "" {
		t.Errorf("event alert = %+v", al)
	}
	mc := one(t, req(KindSentry, sample(t, "sentry-metric-critical.json"), "Sentry-Hook-Resource", "metric_alert"))
	mr := one(t, req(KindSentry, sample(t, "sentry-metric-resolved.json"), "Sentry-Hook-Resource", "metric_alert"))
	if mc.Level != "critical" || mc.Fingerprint != "sentry:metric:7" || mc.Title != "Too many errors" || !strings.Contains(mc.Body, "\nFilter") ||
		!mr.Resolve || mr.Fingerprint != mc.Fingerprint {
		t.Errorf("metric = %+v / %+v", mc, mr)
	}
	if r, _ := Map(req(KindSentry, []byte(`{"action":"created","data":{}}`), "Sentry-Hook-Resource", "installation")); r.Ignored == "" {
		t.Error("installation should be ignored")
	}
	// Signing secret: the right signature passes, a wrong or missing one fails.
	body := sample(t, "sentry-issue-created.json")
	r := req(KindSentry, body, "Sentry-Hook-Resource", "issue", "Sentry-Hook-Signature", sign("client-secret", body))
	r.Secret = "client-secret"
	one(t, r)
	r.Header.Set("Sentry-Hook-Signature", sign("other", body))
	if _, err := Map(r); err != ErrSignature {
		t.Errorf("wrong signature: %v", err)
	}
	r.Header.Del("Sentry-Hook-Signature")
	if _, err := Map(r); err != ErrSignature {
		t.Errorf("missing signature: %v", err)
	}
	if _, err := Map(req(KindSentry, []byte("not json"), "Sentry-Hook-Resource", "issue")); err != ErrPayload {
		t.Errorf("garbage: %v", err)
	}
}

func TestHealthchecks(t *testing.T) {
	down := one(t, req(KindHealthchecks, sample(t, "healthchecks-down.json")))
	if down.Level != "error" || down.Title != "Nightly inventory sync missed its check-in" || down.Fingerprint != "healthchecks:a6c7b0a8a66bed0df66abfdab3c77736861703ee" ||
		!strings.Contains(down.Body, "last ping 2026-10-21") {
		t.Errorf("down = %+v", down)
	}
	up := one(t, req(KindHealthchecks, sample(t, "healthchecks-up.json")))
	if !up.Resolve || up.Fingerprint != down.Fingerprint || up.Title != "Nightly inventory sync is back up" {
		t.Errorf("up = %+v", up)
	}
	// Flat JSON, a form body and query parameters all work; a failed run says so.
	flat := one(t, req(KindHealthchecks, []byte(`{"status":"down","name":"Backup","code":"uuid-1","exitstatus":"2"}`)))
	if flat.Title != "Backup failed (exit status 2)" || flat.Fingerprint != "healthchecks:uuid-1" {
		t.Errorf("flat = %+v", flat)
	}
	form := one(t, req(KindHealthchecks, []byte("status=up&name=Backup&code=uuid-1")))
	q := req(KindHealthchecks, nil)
	q.Query = url.Values{"status": {"down"}, "name": {"Backup"}, "code": {"uuid-1"}}
	query := one(t, q)
	if !form.Resolve || query.Fingerprint != "healthchecks:uuid-1" {
		t.Errorf("form %+v / query %+v", form, query)
	}
	if r, _ := Map(req(KindHealthchecks, []byte(`{"status":"grace"}`))); r.Ignored == "" {
		t.Error("unknown status should be ignored")
	}
}

func TestUptimeKuma(t *testing.T) {
	down := one(t, req(KindUptimeKuma, sample(t, "kuma-down.json"), "Content-Type", "application/json"))
	if down.Level != "error" || down.Title != "Dealer website is down" || down.Fingerprint != "uptime-kuma:12" || down.Body != "connect ECONNREFUSED 203.0.113.10:443" ||
		down.Actions[0].URL != "https://acme-motors.example.com" {
		t.Errorf("down = %+v", down)
	}
	up := one(t, req(KindUptimeKuma, sample(t, "kuma-up.json"), "Content-Type", "application/json"))
	if !up.Resolve || up.Fingerprint != down.Fingerprint || !strings.Contains(up.Body, "was down 12m") {
		t.Errorf("up = %+v", up)
	}
	// form-data preset: the JSON rides in a "data" field.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("data", string(sample(t, "kuma-down.json")))
	mw.Close()
	if fd := one(t, req(KindUptimeKuma, buf.Bytes(), "Content-Type", mw.FormDataContentType())); fd.Fingerprint != down.Fingerprint {
		t.Errorf("form-data = %+v", fd)
	}
	// GET preset: heartbeat and monitor arrive as JSON query parameters.
	var p map[string]json.RawMessage
	_ = json.Unmarshal(sample(t, "kuma-down.json"), &p)
	g := req(KindUptimeKuma, nil)
	g.Method = "GET"
	g.Query = url.Values{"msg": {"x"}, "heartbeat": {string(p["heartbeat"])}, "monitor": {string(p["monitor"])}}
	if gi := one(t, g); gi.Title != "Dealer website is down" {
		t.Errorf("GET = %+v", gi)
	}
	// The Test button: just a message, stored as info.
	if test := one(t, req(KindUptimeKuma, []byte(`{"heartbeat":null,"monitor":null,"msg":"Uptime Kuma Webhook Testing"}`))); test.Level != "info" || test.Resolve {
		t.Errorf("test = %+v", test)
	}
}

func TestGitHub(t *testing.T) {
	fail := one(t, req(KindGitHub, sample(t, "github-failure.json"), "X-GitHub-Event", "workflow_run"))
	if fail.Level != "error" || fail.Title != "Deploy failed on main" || fail.Fingerprint != "github:acme/booking-app:96541:main" ||
		fail.Actions[0].Label != "Open run" || !strings.Contains(fail.Body, "run #118 · by dev-sam") {
		t.Errorf("failure = %+v", fail)
	}
	ok := one(t, req(KindGitHub, sample(t, "github-success.json"), "X-GitHub-Event", "workflow_run"))
	if !ok.Resolve || ok.Fingerprint != fail.Fingerprint || ok.Title != "Deploy passed on main" {
		t.Errorf("success = %+v", ok)
	}
	for _, c := range []struct{ event, body string }{
		{"ping", `{"zen":"Keep it logically awesome."}`},
		{"push", `{}`},
		{"workflow_run", `{"action":"requested","workflow_run":{}}`},
		{"workflow_run", `{"action":"completed","workflow_run":{"conclusion":"cancelled"}}`},
	} {
		if r, err := Map(req(KindGitHub, []byte(c.body), "X-GitHub-Event", c.event)); err != nil || r.Ignored == "" || len(r.Events) != 0 {
			t.Errorf("%s %s should be ignored: %+v %v", c.event, c.body, r, err)
		}
	}
	// The webhook secret is the intake token; a signature from anything else fails.
	body := sample(t, "github-failure.json")
	r := req(KindGitHub, body, "X-GitHub-Event", "workflow_run", "X-Hub-Signature-256", "sha256="+sign("boop_hook_abc", body))
	r.Token = "boop_hook_abc"
	one(t, r)
	r.Header.Set("X-Hub-Signature-256", "sha256="+sign("guess", body))
	if _, err := Map(r); err != ErrSignature {
		t.Errorf("bad signature: %v", err)
	}
}

func TestNightwatch(t *testing.T) {
	op := one(t, req(KindNightwatch, sample(t, "nightwatch-opened.json")))
	if op.Level != "error" || op.Fingerprint != "nightwatch:9d4e2c1a-1234-5678-abcd-ef0123456789" || !strings.HasPrefix(op.Title, `Illuminate\Database\QueryException`) ||
		op.Body != `#42 · production · Illuminate\Database\QueryException · app/Actions/ProcessFlight.php:123` || op.Actions[0].Label != "Open in Nightwatch" {
		t.Errorf("opened = %+v", op)
	}
	rs := one(t, req(KindNightwatch, sample(t, "nightwatch-resolved.json")))
	if !rs.Resolve || rs.Fingerprint != op.Fingerprint || !strings.HasPrefix(rs.Title, "Resolved in Nightwatch") || !strings.Contains(rs.Body, "by Jane Doe") {
		t.Errorf("resolved = %+v", rs)
	}
	if nightwatchLevel("exception", "high") != "critical" || nightwatchLevel("performance", "none") != "warning" || nightwatchLevel("performance", "medium") != "error" {
		t.Error("nightwatchLevel")
	}
	body := sample(t, "nightwatch-opened.json")
	r := req(KindNightwatch, body, "Nightwatch-Signature", sign("nw-secret", body))
	r.Secret = "nw-secret"
	one(t, r)
	r.Body = append(append([]byte{}, body...), ' ')
	if _, err := Map(r); err != ErrSignature {
		t.Errorf("tampered body: %v", err)
	}
}

func TestGenericMapping(t *testing.T) {
	r := req(KindGeneric, sample(t, "generic-batch.json"))
	r.Mapping = sample(t, "generic-mapping.json")
	res, err := Map(r)
	if err != nil || len(res.Events) != 2 {
		t.Fatalf("batch: %+v %v", res, err)
	}
	a, b := res.Events[0], res.Events[1]
	if a.Title != "DiskAlmostFull: Disk 94% full on db-1" || a.Level != "critical" || a.Fingerprint != "a1b2c3" || a.Body != "db-1" ||
		a.Source != "grafana" || a.Actions[0].Label != "Open in Grafana" || a.Resolve {
		t.Errorf("first = %+v", a)
	}
	if !b.Resolve || b.Level != "success" || b.Fingerprint != "d4e5f6" {
		t.Errorf("second (resolved) = %+v", b)
	}
	// Single-object mapping with a level map, fallbacks and the default level.
	m := Mapping{Title: "{{check.name}} {{state|unknown}}", Level: "{{sev}}", LevelMap: map[string]string{"P1": "critical"}, DefaultLevel: "warning"}
	mb, _ := json.Marshal(m)
	r = req(KindGeneric, []byte(`{"check":{"name":"API"},"sev":"p1"}`))
	r.Mapping = mb
	if e := one(t, r); e.Title != "API unknown" || e.Level != "critical" || e.Fingerprint != "webhook:api unknown" {
		t.Errorf("single = %+v", e)
	}
	r.Body = []byte(`{"check":{"name":"API"},"sev":"whatever"}`)
	if e := one(t, r); e.Level != "warning" {
		t.Errorf("default level = %+v", e)
	}
	for _, bad := range []Mapping{{}, {Title: "x", DefaultLevel: "fatal"}, {Title: "x", LevelMap: map[string]string{"a": "loud"}}, {Title: "x", ResolveWhen: &Condition{Field: "s"}}} {
		if bad.Validate() == nil {
			t.Errorf("%+v should be invalid", bad)
		}
	}
	if lookup(map[string]any{"a": []any{map[string]any{"b": "c"}}}, "a.0.b") != "c" || lookup(map[string]any{}, "a.b") != nil {
		t.Error("lookup")
	}
}

func TestMapCleansUp(t *testing.T) {
	m, _ := json.Marshal(Mapping{Title: "{{t}}", URL: "{{u}}"})
	r := req(KindGeneric, []byte(`{"t":"line one\nline two","u":"javascript:alert(1)"}`))
	r.Mapping = m
	e := one(t, r)
	if e.Title != "line one" || len(e.Actions) != 0 {
		t.Errorf("titles are one line and only http(s) links survive: %+v", e)
	}
	long := strings.Repeat("x", 500)
	r.Body = []byte(`{"t":"` + long + `"}`)
	if e := one(t, r); len(e.Title) != events.MaxTitle {
		t.Errorf("title not clipped: %d", len(e.Title))
	}
}
