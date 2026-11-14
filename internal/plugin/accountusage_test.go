package plugin

import "testing"

// Credits are decimal strings. "Inf" and numbers too large for a float64
// parse but are no amount, and JSON cannot carry them, so they read as 0.
func TestCreditsRejectsNonFinite(t *testing.T) {
	for in, want := range map[string]float64{"120.5": 120.5, " 50000 ": 50000, "Inf": 0, "-Inf": 0, "1e400": 0, "NaN": 0, "": 0, "abc": 0} {
		if got := credits(in); got != want {
			t.Errorf("credits(%q) = %v, want %v", in, got, want)
		}
	}
}
