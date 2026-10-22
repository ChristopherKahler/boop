// Package intake turns webhooks from monitoring services into Boop events.
// Each project gets one address per service (/hooks/<kind>/<token>); the
// services do the detecting, the adapters here translate what they send:
// a problem opens or repeats an alert (by fingerprint), a recovery resolves it.
package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/chrisgreg/boop/server/internal/events"
)

// Kinds of intake.
const (
	KindSentry       = "sentry"
	KindHealthchecks = "healthchecks"
	KindUptimeKuma   = "uptime-kuma"
	KindGitHub       = "github"
	KindNightwatch   = "nightwatch"
	KindGeneric      = "generic"
)

// Kinds lists every supported service.
var Kinds = []string{KindSentry, KindHealthchecks, KindUptimeKuma, KindGitHub, KindNightwatch, KindGeneric}

// KindLabel is each kind's display name.
var KindLabel = map[string]string{
	KindSentry:       "Sentry",
	KindHealthchecks: "Healthchecks",
	KindUptimeKuma:   "Uptime Kuma",
	KindGitHub:       "GitHub Actions",
	KindNightwatch:   "Laravel Nightwatch",
	KindGeneric:      "Generic JSON webhook",
}

// ValidKind reports whether k is a supported kind.
func ValidKind(k string) bool { _, ok := KindLabel[k]; return ok }

// Errors a webhook can be rejected with.
var (
	ErrSignature = errors.New("request signature does not match the intake's signing secret")
	ErrPayload   = errors.New("unreadable webhook payload")
)

// Request is one incoming webhook.
type Request struct {
	Kind    string
	Method  string
	Header  http.Header
	Query   url.Values
	Body    []byte
	Token   string // the token from the URL (GitHub can sign with it)
	Secret  string // the intake's stored signing secret, "" when none
	Mapping json.RawMessage
}

// Result is what one webhook turned into.
type Result struct {
	Events []events.Input
	// Ignored says why nothing was stored when Events is empty (a GitHub
	// ping, a workflow that was cancelled, a Sentry installation event...).
	Ignored string
}

func ignored(why string) (Result, error) { return Result{Ignored: why}, nil }

// Map translates a request with the adapter for its kind.
func Map(r Request) (Result, error) {
	var res Result
	var err error
	switch r.Kind {
	case KindSentry:
		res, err = mapSentry(r)
	case KindHealthchecks:
		res, err = mapHealthchecks(r)
	case KindUptimeKuma:
		res, err = mapUptimeKuma(r)
	case KindGitHub:
		res, err = mapGitHub(r)
	case KindNightwatch:
		res, err = mapNightwatch(r)
	case KindGeneric:
		res, err = mapGeneric(r)
	default:
		return Result{}, errors.New("unknown intake kind")
	}
	for i := range res.Events {
		e := &res.Events[i]
		e.Title = clip(strings.TrimSpace(firstLine(e.Title)), events.MaxTitle)
		if e.Title == "" {
			e.Title = KindLabel[r.Kind] + " event"
		}
		e.Body = clip(e.Body, events.MaxBody)
		e.Fingerprint = clip(e.Fingerprint, 200)
		e.ExternalID = clip(e.ExternalID, 200)
		var keep []events.Action
		for _, a := range e.Actions {
			if u, err := url.Parse(a.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && len(a.URL) <= events.MaxActionURL && len(keep) < events.MaxActions {
				a.Label = clip(a.Label, events.MaxActionLabel)
				keep = append(keep, a)
			}
		}
		e.Actions = keep
	}
	return res, err
}

// validHMAC checks a hex HMAC-SHA256 of body under secret; prefix is
// stripped from the header first ("sha256=" for GitHub).
func validHMAC(secret string, body []byte, header, prefix string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), prefix)))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// data marshals v for an event's data field, dropping it when unreasonable.
func data(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil || len(b) > events.MaxDataSize {
		return nil
	}
	return b
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// clip truncates s to max bytes without splitting a rune.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
