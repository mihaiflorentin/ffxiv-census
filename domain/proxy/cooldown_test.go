package proxy

import (
	"testing"
	"time"
)

func TestDestinationBackoff(t *testing.T) {
	cases := []struct {
		name     string
		floor    time.Duration
		cap      time.Duration
		hint     time.Duration
		failures int
		want     time.Duration
	}{
		{"first failure uses the floor", time.Minute, 15 * time.Minute, 0, 0, time.Minute},
		{"doubles per consecutive failure", time.Minute, 15 * time.Minute, 0, 1, 2 * time.Minute},
		{"keeps doubling", time.Minute, 15 * time.Minute, 0, 3, 8 * time.Minute},
		{"saturates at the cap", time.Minute, 15 * time.Minute, 0, 4, 15 * time.Minute},
		{"huge counters do not overflow", time.Minute, 15 * time.Minute, 0, 500, 15 * time.Minute},
		{"retry-after above growth wins", time.Minute, 15 * time.Minute, 10 * time.Minute, 1, 10 * time.Minute},
		{"retry-after beyond the cap is honored", time.Minute, 15 * time.Minute, time.Hour, 0, time.Hour},
		{"cap below floor means no growth", time.Minute, 0, 0, 5, time.Minute},
		{"negative counter is the floor", time.Minute, 15 * time.Minute, 0, -2, time.Minute},
	}
	for _, tc := range cases {
		if got := DestinationBackoff(tc.floor, tc.cap, tc.hint, tc.failures); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
