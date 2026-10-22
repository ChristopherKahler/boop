package levels

import "testing"

func TestAtLeast(t *testing.T) {
	cases := []struct {
		l, min string
		want   bool
	}{
		{Info, Info, true}, {Info, Warning, false}, {Error, Warning, true}, {Critical, Error, true}, {"bogus", Info, true}, {"bogus", Success, false},
	}
	for _, c := range cases {
		if got := AtLeast(c.l, c.min); got != c.want {
			t.Errorf("AtLeast(%s, %s) = %v, want %v", c.l, c.min, got, c.want)
		}
	}
	if Valid("fatal") || !Valid(Critical) {
		t.Error("Valid is wrong")
	}
}

func TestSetHelpers(t *testing.T) {
	got, err := NormaliseSet([]string{"Critical", "warning", "warning"})
	if err != nil || JoinSet(got) != "warning,critical" {
		t.Fatalf("NormaliseSet = %v, %v", got, err)
	}
	if _, err := NormaliseSet([]string{"fatal"}); err == nil {
		t.Error("unknown level accepted")
	}
	if empty, err := NormaliseSet(nil); err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("empty set = %#v, %v", empty, err)
	}
	if JoinSet(From(Error)) != "error,critical" || Lowest([]string{Critical, Warning}) != Warning || Lowest(nil) != "" {
		t.Error("From/Lowest are wrong")
	}
	if len(SplitSet("")) != 0 || JoinSet(SplitSet("info, error")) != "info,error" {
		t.Error("SplitSet is wrong")
	}
	if !Problem(Warning) || Problem(Success) || Max(Warning, Critical) != Critical || Max(Error, Info) != Error {
		t.Error("Problem/Max are wrong")
	}
}
