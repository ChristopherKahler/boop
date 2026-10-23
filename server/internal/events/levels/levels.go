// Package levels defines event severity levels.
package levels

import (
	"fmt"
	"strings"
)

// Levels, in ascending severity.
const (
	Info     = "info"
	Success  = "success"
	Warning  = "warning"
	Error    = "error"
	Critical = "critical"
)

// All lists the levels in ascending severity.
var All = []string{Info, Success, Warning, Error, Critical}

var rank = map[string]int{Info: 0, Success: 1, Warning: 2, Error: 3, Critical: 4}

// Valid reports whether l is a known level.
func Valid(l string) bool {
	_, ok := rank[l]
	return ok
}

// AtLeast reports whether l is at least as severe as min. Unknown values rank lowest.
func AtLeast(l, min string) bool {
	return rank[l] >= rank[min]
}

// Problem reports whether l is a level that opens an alert (warning and above).
func Problem(l string) bool {
	return AtLeast(l, Warning)
}

// Max returns the more severe of a and b.
func Max(a, b string) string {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// From returns every level at least as severe as min, in ascending order.
func From(min string) []string {
	var out []string
	for _, l := range All {
		if AtLeast(l, min) {
			out = append(out, l)
		}
	}
	return out
}

// Lowest returns the least severe level in set, or "" when set is empty.
func Lowest(set []string) string {
	for _, l := range All {
		if Contains(set, l) {
			return l
		}
	}
	return ""
}

// Contains reports whether set holds l.
func Contains(set []string, l string) bool {
	for _, x := range set {
		if x == l {
			return true
		}
	}
	return false
}

// NormaliseSet validates a set of levels and returns it deduplicated in
// ascending severity. An empty set is valid (it means "none").
func NormaliseSet(in []string) ([]string, error) {
	out := []string{}
	for _, l := range All {
		for _, x := range in {
			if strings.ToLower(strings.TrimSpace(x)) == l {
				out = append(out, l)
				break
			}
		}
	}
	for _, x := range in {
		if !Valid(strings.ToLower(strings.TrimSpace(x))) {
			return nil, fmt.Errorf("unknown level %q (use %s)", x, strings.Join(All, ", "))
		}
	}
	return out, nil
}

// JoinSet stores a set as a comma-separated string.
func JoinSet(set []string) string { return strings.Join(set, ",") }

// SplitSet reads a comma-separated set; "" is the empty set.
func SplitSet(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
