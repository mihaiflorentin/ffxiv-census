package contract

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIsProxyAttributable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("db down"), false},
		{"dial failure", fmt.Errorf("fetch: %w", &ProxyCheckError{Kind: CheckProxy}), true},
		{"challenge", fmt.Errorf("fetch: %w", &ProxyCheckError{Kind: CheckTarget, Challenge: true}), true},
		{"destination 429", &ProxyCheckError{Kind: CheckTarget, Status: 429}, true},
		{"server error", &ProxyCheckError{Kind: CheckTarget, Status: 503}, true},
		{"timeout", &ProxyCheckError{Kind: CheckDeadline}, true},
		{"local failure is the job's", &ProxyCheckError{Kind: CheckLocal}, false},
		{"explicit fault wrapper", &ProxyFaultError{Err: errors.New("no proxy available")}, true},
		{"wrapped explicit fault", fmt.Errorf("re-acquire: %w", &ProxyFaultError{Err: errors.New("x")}), true},
		{"text is never parsed", errors.New("proxy check target: status 429"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsProxyAttributable(tt.err); got != tt.want {
				t.Fatalf("IsProxyAttributable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestProxyFaultErrorUnwraps(t *testing.T) {
	cause := errors.New("cause")
	err := &ProxyFaultError{Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("ProxyFaultError must unwrap to its cause")
	}
	if err.Error() == "" {
		t.Fatal("ProxyFaultError needs a message")
	}
}

func TestDecideRetry(t *testing.T) {
	tiers := DefaultRetryTiers()
	jobErr := errors.New("parse failure")
	proxyErr := &ProxyCheckError{Kind: CheckTarget, Challenge: true}

	tests := []struct {
		name         string
		err          error
		attempts     int
		max          int
		wantDead     bool
		wantAttempts int
		wantProxy    bool
		wantTier     int
	}{
		{"first job failure uses the 5s tier", jobErr, 0, 50, false, 1, false, 0},
		{"second job failure uses the 30s tier", jobErr, 1, 50, false, 2, false, 1},
		{"third job failure uses the 5m tier", jobErr, 2, 50, false, 3, false, 2},
		{"fourth job failure uses the 1h tier", jobErr, 3, 50, false, 4, false, 3},
		{"deep job failures stay on the 1h tier", jobErr, 30, 50, false, 31, false, 3},
		{"first proxy failure does not count and uses 5s", proxyErr, 0, 50, false, 0, true, 0},
		{"proxy failure keeps the tier of the job's last attempt", proxyErr, 3, 50, false, 3, true, 2},
		{"proxy failures never reach the attempt cap", proxyErr, 49, 50, false, 49, true, 3},
		{"attempt cap dead-parks a job failure", jobErr, 49, 50, true, 50, false, 0},
		{"poison payload dead-parks at once", errors.Join(ErrPoisonPayload, jobErr), 0, 50, true, 1, false, 0},
		{"no handler dead-parks at once", fmt.Errorf("x: %w", ErrNoHandler), 0, 50, true, 1, false, 0},
		{"poison wins over proxy attribution", errors.Join(ErrPoisonPayload, proxyErr), 0, 50, true, 0, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideRetry(tt.err, tt.attempts, 0, tt.max, 20, tiers)
			if got.Dead != tt.wantDead {
				t.Fatalf("Dead = %v, want %v", got.Dead, tt.wantDead)
			}
			if got.Dead {
				return
			}
			if got.Attempts != tt.wantAttempts || got.ProxyFault != tt.wantProxy || got.Tier != tt.wantTier {
				t.Fatalf("got attempts=%d proxy=%v tier=%d, want attempts=%d proxy=%v tier=%d",
					got.Attempts, got.ProxyFault, got.Tier, tt.wantAttempts, tt.wantProxy, tt.wantTier)
			}
			if got.Delay != tiers[tt.wantTier] {
				t.Fatalf("Delay = %s, want %s", got.Delay, tiers[tt.wantTier])
			}
		})
	}
}

func TestDecideRetry_ProxyFailuresPastTheCapCountAsAttempts(t *testing.T) {
	tiers := DefaultRetryTiers()
	proxyErr := &ProxyCheckError{Kind: CheckTarget, Challenge: true}
	tests := []struct {
		name          string
		attempts      int
		proxyFailures int
		max           int
		cap           int
		wantDead      bool
		wantAttempts  int
		wantProxy     bool
	}{
		{"below the cap the failure is still the proxy's", 0, 18, 50, 20, false, 0, true},
		{"the failure that reaches the cap is still the proxy's", 0, 19, 50, 20, false, 0, true},
		{"the first failure past the cap counts as an attempt", 0, 20, 50, 20, false, 1, false},
		{"further failures keep counting and climb the ladder", 2, 35, 50, 20, false, 3, false},
		{"past the cap the attempt limit dead-parks", 49, 20, 50, 20, true, 50, false},
		{"a zero cap never converts proxy failures", 0, 1000, 50, 0, false, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideRetry(proxyErr, tt.attempts, tt.proxyFailures, tt.max, tt.cap, tiers)
			if got.Dead != tt.wantDead {
				t.Fatalf("Dead = %v, want %v", got.Dead, tt.wantDead)
			}
			if got.Attempts != tt.wantAttempts || got.ProxyFault != tt.wantProxy {
				t.Fatalf("got attempts=%d proxy=%v, want attempts=%d proxy=%v", got.Attempts, got.ProxyFault, tt.wantAttempts, tt.wantProxy)
			}
		})
	}
}

func TestDecideRetry_JobFailuresIgnoreTheProxyFailureCount(t *testing.T) {
	got := DecideRetry(errors.New("parse failure"), 0, 99, 50, 20, DefaultRetryTiers())
	if got.Attempts != 1 || got.ProxyFault || got.Dead {
		t.Fatalf("got %+v, want a plain first job failure", got)
	}
}

func TestDecideRetry_FewerTiersThanLadderSteps(t *testing.T) {
	got := DecideRetry(errors.New("x"), 5, 0, 50, 20, []time.Duration{time.Second, time.Minute})
	if got.Tier != 1 || got.Delay != time.Minute {
		t.Fatalf("got tier %d delay %s, want the last tier", got.Tier, got.Delay)
	}
}

func TestDecideRetry_NoTiersFallsBackToDefaults(t *testing.T) {
	got := DecideRetry(errors.New("x"), 0, 0, 50, 20, nil)
	if got.Delay != DefaultRetryTiers()[0] {
		t.Fatalf("Delay = %s, want first default tier", got.Delay)
	}
}

func TestDecideLegacyDrain(t *testing.T) {
	tiers := DefaultRetryTiers()
	tests := []struct {
		name       string
		attempts   int
		remaining  time.Duration
		wantDead   bool
		wantDirect bool
		wantTier   int
	}{
		{"elapsed backoff republishes straight to main", 3, 0, false, true, 0},
		{"short remaining wait goes to the 5s tier", 3, 4 * time.Second, false, false, 0},
		{"wait just above a tier rounds up", 3, 6 * time.Second, false, false, 1},
		{"a ten minute wait uses the 1h tier", 3, 10 * time.Minute, false, false, 3},
		{"a wait beyond every tier uses the longest", 3, 5 * time.Hour, false, false, 3},
		{"exhausted attempts dead-park", 50, 0, true, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideLegacyDrain(tt.attempts, 50, tt.remaining, tiers)
			if got.Dead != tt.wantDead || got.Direct != tt.wantDirect || (!got.Dead && !got.Direct && got.Tier != tt.wantTier) {
				t.Fatalf("got %+v, want dead=%v direct=%v tier=%d", got, tt.wantDead, tt.wantDirect, tt.wantTier)
			}
		})
	}
}
