package auth

import (
	"testing"
	"time"
)

func TestActionTokens(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	tok := SignAction(secret, "alr_1", "acknowledge", now.Add(time.Hour))

	if id, act, err := VerifyAction(secret, tok, now); err != nil || id != "alr_1" || act != "acknowledge" {
		t.Fatalf("verify: %q %q %v", id, act, err)
	}
	if _, _, err := VerifyAction(secret, tok, now.Add(2*time.Hour)); err == nil {
		t.Error("expired token accepted")
	}
	if _, _, err := VerifyAction([]byte("another secret, another server!!"), tok, now); err == nil {
		t.Error("token from another secret accepted")
	}
	// Swap in another alert id but keep the signature: must fail.
	forged := SignAction(secret, "alr_2", "acknowledge", now.Add(time.Hour))
	if _, _, err := VerifyAction(secret, forged[:len(forged)/2]+tok[len(tok)/2:], now); err == nil {
		t.Error("spliced token accepted")
	}
	for _, bad := range []string{"", "x", "a.b", tok + "x"} {
		if _, _, err := VerifyAction(secret, bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
