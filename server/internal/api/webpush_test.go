package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chrisgreg/boop/server/internal/auth"
	"github.com/chrisgreg/boop/server/internal/webpush"
)

// fakePushService stands in for FCM / Mozilla / Apple's push service and
// keeps what it receives so the test can decrypt it like a phone would.
type fakePushService struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newFakePushService(t *testing.T) *fakePushService {
	f := &fakePushService{status: http.StatusCreated}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.bodies = append(f.bodies, b)
		w.WriteHeader(f.status)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePushService) received() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bodies...)
}

// phone is a browser's side of a push subscription.
type phone struct {
	priv *ecdh.PrivateKey
	auth []byte
	sub  map[string]any
}

func newPhone(t *testing.T, endpoint string) phone {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	enc := base64.RawURLEncoding
	return phone{priv: priv, auth: a, sub: map[string]any{"endpoint": endpoint, "keys": map[string]string{
		"p256dh": enc.EncodeToString(priv.PublicKey().Bytes()), "auth": enc.EncodeToString(a)}}}
}

func (p phone) read(t *testing.T, body []byte) map[string]any {
	t.Helper()
	pt, err := webpush.Decrypt(p.priv, p.auth, body)
	if err != nil {
		t.Fatalf("phone could not decrypt the push: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(pt, &out); err != nil {
		t.Fatalf("payload is not JSON: %s", pt)
	}
	return out
}

func TestWebPushEndToEnd(t *testing.T) {
	e := newEnv(t)
	push := newFakePushService(t)
	pub, priv, _ := webpush.GenerateKeys()
	keys, _ := webpush.ParseKeys(pub, priv)
	client := webpush.NewClient(keys, "mailto:ops@example.com")
	client.SetHTTPClient(push.Client())
	secret := []byte("test-action-secret-0123456789abc")
	e.server.WebPush, e.server.ActionSecret = client, secret
	e.server.Dispatcher.SetWeb(client, func(id string) string {
		return auth.SignAction(secret, id, "acknowledge", time.Now().Add(time.Hour))
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.server.Dispatcher.Start(ctx)

	r := e.do("GET", "/api/v1/webpush", "", nil)
	if r.status != 200 || r.body["configured"] != true || r.body["public_key"] != pub {
		t.Fatalf("webpush info: %d %s", r.status, r.raw)
	}
	if st := e.do("GET", "/api/v1/status", "", nil); st.body["push_ready"] != true {
		t.Errorf("status should report push ready: %s", st.raw)
	}

	// Subscribe this "phone"; subscribing again updates the same device.
	ph := newPhone(t, push.URL+"/fcm/send/abc")
	r = e.do("POST", "/api/v1/webpush/subscriptions", "", map[string]any{"subscription": ph.sub, "name": "Galaxy A15"})
	if r.status != 201 || r.body["platform"] != "web" || r.body["push_registered"] != true {
		t.Fatalf("subscribe: %d %s", r.status, r.raw)
	}
	devID := r.body["id"]
	r = e.do("POST", "/api/v1/webpush/subscriptions", "", map[string]any{"subscription": ph.sub, "name": "Chris's phone"})
	if r.body["id"] != devID || r.body["name"] != "Chris's phone" {
		t.Errorf("resubscribe made a new device: %s", r.raw)
	}
	bad := newPhone(t, "http://insecure.example/x")
	if r := e.do("POST", "/api/v1/webpush/subscriptions", "", map[string]any{"subscription": bad.sub}); r.status != 422 {
		t.Errorf("insecure endpoint accepted: %d", r.status)
	}

	// An alert reaches the phone, encrypted, with an Acknowledge button.
	e.do("POST", "/api/v1/clients", "", map[string]any{"name": "Acme"})
	key := e.do("POST", "/api/v1/projects", "", map[string]any{"name": "Site", "client_id": "acme"}).body["api_key"].(string)
	ev := e.do("POST", "/api/v1/events", key, map[string]any{"title": "Site down", "level": "critical", "fingerprint": "kuma:1",
		"actions": []map[string]string{{"label": "Open status page", "url": "https://status.example.com"}}}).body
	alertID := ev["alert_id"].(string)
	deadline := time.Now().Add(3 * time.Second)
	for len(push.received()) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := push.received()
	if len(got) != 1 {
		t.Fatalf("pushes received = %d", len(got))
	}
	msg := ph.read(t, got[0])
	if msg["title"] != "Acme · Site down" || msg["tag"] != alertID || msg["url"] != "/alerts/"+alertID || msg["require_interaction"] != true || msg["ack"] == "" {
		t.Fatalf("payload = %v", msg)
	}
	acts := msg["actions"].([]any)
	if len(acts) != 2 || acts[0].(map[string]any)["action"] != "ack" || acts[1].(map[string]any)["url"] != "https://status.example.com" {
		t.Errorf("actions = %v", acts)
	}
	if dl := e.do("GET", "/api/v1/events/"+ev["id"].(string)+"/deliveries", "", nil); dl.body["deliveries"].([]any)[0].(map[string]any)["status"] != "sent" {
		t.Errorf("delivery not recorded as sent: %s", dl.raw)
	}

	// Pressing Acknowledge on the notification works without a login.
	r = e.do("POST", "/api/v1/push/action", "", map[string]string{"token": msg["ack"].(string)})
	if r.status != 200 || r.body["status"] != "acknowledged" || r.body["acknowledged_by"] != "phone (notification)" {
		t.Fatalf("ack from notification: %d %s", r.status, r.raw)
	}
	if r := e.do("POST", "/api/v1/push/action", "", map[string]string{"token": "forged.token"}); r.status != 401 {
		t.Errorf("forged token: %d", r.status)
	}
	other := auth.SignAction([]byte("another server's secret!!!!!!!!!"), alertID, "acknowledge", time.Now().Add(time.Hour))
	if r := e.do("POST", "/api/v1/push/action", "", map[string]string{"token": other}); r.status != 401 {
		t.Errorf("token from another secret: %d", r.status)
	}

	// The push service says the subscription is gone: the device stops getting pushes.
	push.mu.Lock()
	push.status = http.StatusGone
	push.mu.Unlock()
	e.do("POST", "/api/v1/events", key, map[string]any{"title": "Other thing", "level": "error", "fingerprint": "x"})
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d := e.do("GET", "/api/v1/devices", "", nil); d.body["devices"].([]any)[0].(map[string]any)["push_registered"] == false {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if d := e.do("GET", "/api/v1/devices", "", nil); d.body["devices"].([]any)[0].(map[string]any)["push_registered"] != false {
		t.Errorf("gone subscription still pushable: %s", d.raw)
	}

	if r := e.do("DELETE", "/api/v1/webpush/subscriptions", "", map[string]string{"endpoint": push.URL + "/nope"}); r.status != 404 {
		t.Errorf("unsubscribe unknown: %d", r.status)
	}
}

func TestWebPushUnavailable(t *testing.T) {
	e := newEnv(t)
	e.server.WebPushError = "bad key"
	ph := newPhone(t, "https://push.example/x")
	r := e.do("POST", "/api/v1/webpush/subscriptions", "", map[string]any{"subscription": ph.sub})
	if r.status != 503 {
		t.Errorf("subscribe without web push: %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/v1/webpush", "", nil); r.body["configured"] != false || r.body["error"] != "bad key" {
		t.Errorf("info: %s", r.raw)
	}
}
