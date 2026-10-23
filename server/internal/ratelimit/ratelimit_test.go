package ratelimit

import (
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time              { return c.t }
func (c *clock) add(d time.Duration)         { c.t = c.t.Add(d) }
func newAt(perMinute int, c *clock) *Limiter { l := New(perMinute); l.now = c.now; return l }

func TestBurstThenRefuse(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(6, c) // one every 10 seconds, bursts of 6
	for i := 0; i < 6; i++ {
		if r := l.Allow("a"); !r.OK {
			t.Fatalf("request %d of the first 6 refused", i+1)
		}
	}
	r := l.Allow("a")
	if r.OK || !r.First {
		t.Fatalf("7th request: %+v, want refused and first", r)
	}
	if r.RetryAfter != 10*time.Second {
		t.Errorf("retry after %v, want 10s", r.RetryAfter)
	}
	if r := l.Allow("a"); r.OK || r.First {
		t.Errorf("8th request: %+v, want refused and not first", r)
	}
	if r := l.Allow("b"); !r.OK {
		t.Error("another key shares the first key's bucket")
	}
}

func TestRefillsOverTheMinute(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(6, c)
	for i := 0; i < 6; i++ {
		l.Allow("a")
	}
	c.add(9 * time.Second)
	if l.Allow("a").OK {
		t.Error("allowed before a full token refilled")
	}
	c.add(time.Second)
	if r := l.Allow("a"); !r.OK {
		t.Errorf("refused after 10s refill: %+v", r)
	}
	if l.Allow("a").OK {
		t.Error("one refill allowed two requests")
	}
	c.add(time.Hour)
	n := 0
	for l.Allow("a").OK {
		n++
	}
	if n != 6 {
		t.Errorf("after a long idle, burst of %d, want the cap of 6", n)
	}
}

func TestOffAndNil(t *testing.T) {
	if New(7).PerMinute() != 7 {
		t.Error("PerMinute does not report the limit")
	}
	for _, l := range []*Limiter{nil, New(0), New(-1)} {
		if l.PerMinute() != 0 {
			t.Errorf("an off limiter reports %d a minute", l.PerMinute())
		}
		for i := 0; i < 1000; i++ {
			if !l.Allow("a").OK {
				t.Fatalf("limiter %v refused", l)
			}
		}
	}
}

func TestFullKeysAreForgotten(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newAt(6, c)
	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	c.add(55 * time.Second)
	for i := 0; i < 6; i++ {
		l.Allow("a") // a is busy: its allowance is full again only at +115s
	}
	c.add(6 * time.Second) // +61s: b and c have been full since +10s
	l.Allow("d")
	if l.Len() != 2 {
		t.Errorf("tracking %d keys after the sweep, want 2 (a, d)", l.Len())
	}
	if l.Allow("a").OK {
		t.Error("the sweep forgot a key that was still limited")
	}
}
