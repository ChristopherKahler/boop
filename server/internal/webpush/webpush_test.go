package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RFC 8291 Appendix A: fixed keys and salt must give exactly the published
// header and ciphertext.
func TestEncryptMatchesRFC8291(t *testing.T) {
	plaintext := mustDecode(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	asPriv, err := ecdh.P256().NewPrivateKey(mustDecode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPublic := mustDecode(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	salt := mustDecode(t, "DGv6ra1nlYgDCS1FRnbzlw")
	auth := mustDecode(t, "BTBZMqHH6r4Tts7J_aSIgg")

	got, err := encrypt(uaPublic, auth, plaintext, salt, asPriv)
	if err != nil {
		t.Fatal(err)
	}
	header := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z 9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml mlMoZIIgDll6e3vCYLocInmYWAmS6Tlz AC8wEqKK6PBru3jl7A8"
	ct := "8pfeW0KbunFT06SuDKoJH9Ql87S1QUrd irN6GcG7sFz1y1sqLgVi1VhjVkHsUoEs bI_0LpXMuGvnzQ"
	want := append(mustDecode(t, header), mustDecode(t, ct)...)
	if b64.EncodeToString(got) != b64.EncodeToString(want) {
		t.Fatalf("encrypt mismatch\n got  %s\n want %s", b64.EncodeToString(got), b64.EncodeToString(want))
	}
}

// decrypt runs the exported receiver side and fails the test on error.
func decrypt(t *testing.T, uaPriv *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	pt, err := Decrypt(uaPriv, auth, body)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return pt
}

func newSubscription(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey, []byte) {
	t.Helper()
	uaPriv, err := ecdh.P256().GenerateKey(nil)
	if err != nil {
		uaPriv, _ = ecdh.P256().NewPrivateKey(mustDecode(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	}
	auth := []byte("0123456789abcdef")
	var s Subscription
	s.Endpoint = endpoint
	s.Keys.P256dh = b64.EncodeToString(uaPriv.PublicKey().Bytes())
	s.Keys.Auth = b64.EncodeToString(auth)
	return s, uaPriv, auth
}

func TestSendEncryptsSignsAndReportsGone(t *testing.T) {
	pub, priv, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeys(pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKeys("BAAA", priv); err == nil {
		t.Error("mismatched public key accepted")
	}

	var got *http.Request
	var body []byte
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Location", "https://push.example/m/1")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	c := NewClient(keys, "mailto:ops@example.com")
	c.http = srv.Client()
	sub, uaPriv, auth := newSubscription(t, srv.URL+"/push/abc")
	loc, err := c.Send(context.Background(), sub, []byte(`{"title":"hi"}`), Options{TTL: time.Hour, Urgency: "high", Topic: "alr_1"})
	if err != nil || loc != "https://push.example/m/1" {
		t.Fatalf("send: %q %v", loc, err)
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("TTL") != "3600" || got.Header.Get("Urgency") != "high" || got.Header.Get("Topic") != "alr_1" {
		t.Errorf("headers: %v", got.Header)
	}
	if pt := decrypt(t, uaPriv, auth, body); string(pt) != `{"title":"hi"}` {
		t.Errorf("round trip = %q", pt)
	}

	// The VAPID header carries a JWT signed by our key for the endpoint's origin.
	a := got.Header.Get("Authorization")
	if !strings.HasPrefix(a, "vapid t=") || !strings.HasSuffix(a, ", k="+keys.Public) {
		t.Fatalf("authorization = %q", a)
	}
	jwt := strings.TrimSuffix(strings.TrimPrefix(a, "vapid t="), ", k="+keys.Public)
	parts := strings.Split(jwt, ".")
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	_ = json.Unmarshal(mustDecode(t, parts[1]), &claims)
	if claims.Aud != srv.URL || claims.Sub != "mailto:ops@example.com" || claims.Exp < time.Now().Add(11*time.Hour).Unix() {
		t.Errorf("claims = %+v", claims)
	}
	sig := mustDecode(t, parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	pk, _ := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustDecode(t, keys.Public))
	if !ecdsa.Verify(pk, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("VAPID signature does not verify")
	}

	// A vanished subscription is reported as Gone.
	status = http.StatusGone
	_, err = c.Send(context.Background(), sub, []byte("x"), Options{})
	var we *Error
	if !errorsAs(err, &we) || !we.Gone() {
		t.Errorf("410 should be Gone: %v", err)
	}
}

func TestValidateAndLimits(t *testing.T) {
	sub, _, _ := newSubscription(t, "http://insecure.example/x")
	if sub.Validate() == nil {
		t.Error("http endpoint accepted")
	}
	sub.Endpoint = "https://fcm.googleapis.com/fcm/send/abc"
	if err := sub.Validate(); err != nil {
		t.Errorf("valid subscription rejected: %v", err)
	}
	sub.Keys.Auth = "AAAA"
	if sub.Validate() == nil {
		t.Error("short auth accepted")
	}
	sub, _, _ = newSubscription(t, "https://x.example/y")
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err == nil {
		t.Error("oversized payload accepted")
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload)); err != nil {
		t.Errorf("max payload rejected: %v", err)
	}
}

func errorsAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}
