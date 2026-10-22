package ids

import (
	"testing"
	"time"
)

// Cursor pagination and newest-first ordering compare created_at strings, so
// two calls to Now must never return the same value, even on platforms whose
// clock ticks coarsely (Windows).
func TestNowIsStrictlyIncreasing(t *testing.T) {
	prev := Now()
	for i := 0; i < 20000; i++ {
		cur := Now()
		if cur <= prev {
			t.Fatalf("call %d: Now() = %s, not after previous %s", i, cur, prev)
		}
		prev = cur
	}
}

func TestNowStaysCloseToTheClock(t *testing.T) {
	got, err := Parse(Now())
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(got); d < -time.Second || d > time.Second {
		t.Fatalf("Now() drifted %v from the wall clock", d)
	}
}
