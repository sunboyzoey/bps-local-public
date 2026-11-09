package retryafter

import "testing"

func TestBoundedHints(t *testing.T) {
	for raw, want := range map[string]int{"17": 17, "-1": 0, "secret": 0, "9999999": 604800, "": 0} {
		if got := Seconds(raw); got != want {
			t.Fatalf("%q: %d", raw, got)
		}
	}
}
