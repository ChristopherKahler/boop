package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrisgreg/boop/server/internal/ratelimit"
)

// /hooks is published to the internet on its own, so nothing under it may
// fall through to the web UI.
func TestHookPathsNeverServeTheUI(t *testing.T) {
	e := newEnv(t)
	withUI := *e.server
	withUI.Web = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<!doctype html><title>Boop</title>"))
	})
	srv := httptest.NewServer(withUI.Handler())
	t.Cleanup(srv.Close)
	for _, method := range []string{"GET", "POST", "PUT"} {
		for _, path := range []string{"/hooks", "/hooks/", "/hooks/sentry", "/hooks/a/b/c", "/hooks/uptime-kuma/boop_hook_nope"} {
			req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader("{}"))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 404 || !strings.Contains(string(raw), `"no such intake"`) {
				t.Errorf("%s %s: %d %s; want the wrong-address 404", method, path, res.StatusCode, raw)
			}
		}
	}
	if res, _ := http.Get(srv.URL + "/"); res != nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(raw), "<!doctype html>") {
			t.Errorf("the web UI itself is not served at /: %s", raw)
		}
	}
}

func TestHookRateLimits(t *testing.T) {
	e := newEnv(t)
	pid, _ := e.createProject("Site")
	newIntake := func() string {
		r := e.do("POST", "/api/v1/projects/"+pid+"/intakes", "", map[string]any{"kind": "uptime-kuma"})
		if r.status != 201 {
			t.Fatalf("create intake: %d %s", r.status, r.raw)
		}
		return r.body["url"].(string)
	}
	a, b := newIntake(), newIntake()

	// A second server on the same database, with low limits; the first one
	// keeps none.
	limited := *e.server
	limited.HookAddresses = ratelimit.New(3)
	limited.HookSenders = ratelimit.New(5)
	srv := httptest.NewServer(limited.Handler())
	t.Cleanup(srv.Close)
	at := func(url string) string { return strings.Replace(url, e.srv.URL, srv.URL, 1) }
	sample := hookSample(t, "kuma-down.json")
	send := func(url, sender string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", url, bytes.NewReader(sample))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", sender)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	// One address: 3 a minute, then 429 with a Retry-After of 20 seconds
	// (the allowance comes back one request every 60/3 seconds).
	for i := 1; i <= 3; i++ {
		if res := send(at(a), "203.0.113.1"); res.StatusCode != 200 {
			t.Fatalf("request %d to address A: %d", i, res.StatusCode)
		}
	}
	res := send(at(a), "203.0.113.2")
	if res.StatusCode != 429 || res.Header.Get("Retry-After") != "20" {
		t.Fatalf("4th request to A from another sender: %d, Retry-After %q; want 429 and 20", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res := send(at(b), "203.0.113.2"); res.StatusCode != 200 {
		t.Errorf("address B is limited by A's flood: %d", res.StatusCode)
	}
	r := e.do("GET", "/api/v1/intakes", "", nil)
	if !strings.Contains(string(r.raw), "rate limited: more than 3 a minute") {
		t.Errorf("intake A does not show it was rate limited: %s", r.raw)
	}

	// One sender: 5 a minute across all addresses, wrong ones included, so
	// guessing addresses gets cut off too.
	for i := 1; i <= 5; i++ {
		if res := send(srv.URL+"/hooks/uptime-kuma/boop_hook_guess", "198.51.100.7"); res.StatusCode != 404 {
			t.Fatalf("guess %d: %d", i, res.StatusCode)
		}
	}
	if res := send(at(b), "198.51.100.7"); res.StatusCode != 429 {
		t.Errorf("a sender over its limit still got through to B: %d", res.StatusCode)
	}
	if res := send(at(b), "198.51.100.8"); res.StatusCode != 200 {
		t.Errorf("another sender to B: %d", res.StatusCode)
	}

	// A server with no limiters never refuses.
	for i := 0; i < 10; i++ {
		if r := e.post(a, sample); r.status != 200 {
			t.Fatalf("no-limit server refused request %d: %d", i+1, r.status)
		}
	}
}
