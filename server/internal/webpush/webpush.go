// Package webpush sends Web Push notifications (RFC 8030) to browsers and
// installed web apps, with VAPID authentication (RFC 8292) and aes128gcm
// payload encryption (RFC 8291, RFC 8188). It uses only the standard library.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var b64 = base64.RawURLEncoding

// decode accepts base64url or standard base64, padded or not: browsers and
// libraries are inconsistent about which they hand out.
func decode(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return b64.DecodeString(s)
}

// Keys is a VAPID key pair. Public is what browsers take as the
// applicationServerKey (base64url, uncompressed P-256 point).
type Keys struct {
	priv   *ecdsa.PrivateKey
	Public string
}

// GenerateKeys returns a new VAPID key pair as base64url strings.
func GenerateKeys() (public, private string, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	priv, err := k.Bytes()
	if err != nil {
		return "", "", err
	}
	return b64.EncodeToString(pub), b64.EncodeToString(priv), nil
}

// ParseKeys loads a VAPID key pair and checks the halves belong together.
func ParseKeys(public, private string) (*Keys, error) {
	raw, err := decode(private)
	if err != nil {
		return nil, fmt.Errorf("VAPID private key is not base64url: %w", err)
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID private key: %w", err)
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	enc := b64.EncodeToString(pub)
	if public != "" {
		given, err := decode(public)
		if err != nil || !bytes.Equal(given, pub) {
			return nil, errors.New("VAPID public key does not match the private key")
		}
	}
	return &Keys{priv: k, Public: enc}, nil
}

// Subscription is what PushManager.subscribe() returns in the browser.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// Validate checks the endpoint and keys are well formed.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("subscription endpoint must be an https URL")
	}
	if len(s.Endpoint) > 1024 {
		return errors.New("subscription endpoint is too long")
	}
	p, err := decode(s.Keys.P256dh)
	if err != nil || len(p) != 65 || p[0] != 4 {
		return errors.New("subscription p256dh must be an uncompressed P-256 public key")
	}
	a, err := decode(s.Keys.Auth)
	if err != nil || len(a) != 16 {
		return errors.New("subscription auth secret must be 16 bytes")
	}
	return nil
}

// recordSize is the aes128gcm record size; one record carries the payload.
const recordSize = 4096

// MaxPayload is the largest plaintext that fits one record.
const MaxPayload = recordSize - 86 - 16 - 1

// Encrypt encrypts plaintext for sub using aes128gcm (RFC 8291).
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	uaPublic, err := decode(sub.Keys.P256dh)
	if err != nil {
		return nil, err
	}
	auth, err := decode(sub.Keys.Auth)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(uaPublic, auth, plaintext, salt, asPriv)
}

// encrypt is Encrypt with the salt and sender key supplied (for the RFC 8291
// Appendix A test vector).
func encrypt(uaPublic, authSecret, plaintext, salt []byte, asPriv *ecdh.PrivateKey) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("payload is %d bytes; the limit is %d", len(plaintext), MaxPayload)
	}
	uaPub, err := ecdh.P256().NewPublicKey(uaPublic)
	if err != nil {
		return nil, fmt.Errorf("subscription key: %w", err)
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPublic := asPriv.PublicKey().Bytes()

	// IKM = HKDF(auth_secret, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPublic...), asPublic...)
	prkKey, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// Header: salt(16) || rs(4) || idlen(1) || keyid(as_public)
	out := make([]byte, 0, 86+len(plaintext)+1+16)
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	padded := append(append([]byte{}, plaintext...), 0x02) // last-record delimiter
	return gcm.Seal(out, nonce, padded, nil), nil
}

// Decrypt reverses Encrypt on the receiving side (what a browser does with
// its subscription's private key and auth secret). Boop itself never needs
// it; tests and the end-to-end checker use it to read what a phone would see.
func Decrypt(uaPriv *ecdh.PrivateKey, authSecret, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("body too short")
	}
	salt, idlen := body[:16], int(body[20])
	if len(body) < 21+idlen+16 {
		return nil, errors.New("body too short")
	}
	asPublic, ct := body[21:21+idlen], body[21+idlen:]
	asPub, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...), asPublic...)
	prkKey, err := hkdf.Extract(sha256.New, secret, authSecret)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	pt = bytes.TrimRight(pt, "\x00")
	if len(pt) == 0 || pt[len(pt)-1] != 0x02 {
		return nil, errors.New("missing record delimiter")
	}
	return pt[:len(pt)-1], nil
}

// Error is a non-2xx answer from a push service.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("webpush: %d %s", e.Status, e.Body)
	}
	return fmt.Sprintf("webpush: %d", e.Status)
}

// Gone reports whether the subscription no longer exists and should be dropped.
func (e *Error) Gone() bool { return e.Status == http.StatusNotFound || e.Status == http.StatusGone }

// Options tune one message.
type Options struct {
	TTL     time.Duration // how long the push service keeps it for an offline device
	Urgency string        // very-low | low | normal | high
	Topic   string        // a newer message with the same topic replaces an undelivered one
}

// Client sends messages with one VAPID identity.
type Client struct {
	keys    *Keys
	subject string
	http    *http.Client
	now     func() time.Time

	mu     sync.Mutex
	tokens map[string]cachedToken // per push-service origin
}

type cachedToken struct {
	jwt string
	exp time.Time
}

// NewClient returns a Client. subject is the contact the push services see:
// a mailto: address or an https URL.
func NewClient(keys *Keys, subject string) *Client {
	return &Client{keys: keys, subject: subject, http: &http.Client{Timeout: 15 * time.Second}, now: time.Now, tokens: map[string]cachedToken{}}
}

// SetHTTPClient replaces the HTTP client (tests point it at a fake push service).
func (c *Client) SetHTTPClient(h *http.Client) { c.http = h }

// PublicKey is the applicationServerKey browsers subscribe with.
func (c *Client) PublicKey() string { return c.keys.Public }

// Send encrypts payload for sub and posts it to the push service. It returns
// the message location the service reports, if any.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) (string, error) {
	if err := sub.Validate(); err != nil {
		return "", err
	}
	body, err := Encrypt(sub, payload)
	if err != nil {
		return "", err
	}
	auth, err := c.authorization(sub.Endpoint)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	if o.Urgency != "" {
		req.Header.Set("Urgency", o.Urgency)
	}
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", &Error{Status: res.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	return res.Header.Get("Location"), nil
}

// authorization returns the VAPID header for the endpoint's origin, reusing
// a signed token until an hour before it expires.
func (c *Client) authorization(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	aud := u.Scheme + "://" + u.Host
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.tokens[aud]; ok && now.Before(t.exp.Add(-time.Hour)) {
		return "vapid t=" + t.jwt + ", k=" + c.keys.Public, nil
	}
	exp := now.Add(12 * time.Hour)
	jwt, err := c.sign(aud, exp)
	if err != nil {
		return "", err
	}
	c.tokens[aud] = cachedToken{jwt: jwt, exp: exp}
	return "vapid t=" + jwt + ", k=" + c.keys.Public, nil
}

func (c *Client) sign(aud string, exp time.Time) (string, error) {
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{"aud": aud, "exp": exp.Unix(), "sub": c.subject})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.keys.priv, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return unsigned + "." + b64.EncodeToString(sig), nil
}
