package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrBadActionToken covers malformed, forged and expired action tokens.
var ErrBadActionToken = errors.New("action token is invalid or has expired")

var actionEnc = base64.RawURLEncoding

// SignAction returns a token that lets its holder perform one action on one
// alert until exp, without a login. It rides inside a push notification so the
// phone's "Acknowledge" button works even when the app's session has lapsed.
func SignAction(secret []byte, alertID, action string, exp time.Time) string {
	payload := alertID + "|" + action + "|" + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return actionEnc.EncodeToString([]byte(payload)) + "." + actionEnc.EncodeToString(mac.Sum(nil))
}

// VerifyAction checks a token and returns the alert id and action it grants.
func VerifyAction(secret []byte, token string, now time.Time) (alertID, action string, err error) {
	p, s, ok := strings.Cut(token, ".")
	if !ok {
		return "", "", ErrBadActionToken
	}
	payload, err1 := actionEnc.DecodeString(p)
	sig, err2 := actionEnc.DecodeString(s)
	if err1 != nil || err2 != nil {
		return "", "", ErrBadActionToken
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", ErrBadActionToken
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		return "", "", ErrBadActionToken
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", "", ErrBadActionToken
	}
	return parts[0], parts[1], nil
}
