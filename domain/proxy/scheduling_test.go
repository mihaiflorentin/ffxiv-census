package proxy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestRecoveryDelaySaturates(t *testing.T) {
	for step, want := range []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		15 * time.Minute, 15 * time.Minute,
	} {
		if got := RecoveryDelay(step, time.Minute, 15*time.Minute); got != want {
			t.Fatalf("step %d: %v, want %v", step, got, want)
		}
	}
	if got := RecoveryDelay(1<<30, time.Minute, 15*time.Minute); got != 15*time.Minute {
		t.Fatalf("large failure history overflowed: %v", got)
	}
}

func TestClassifyCheck(t *testing.T) {
	stable := contract.GuardSnapshot{Healthy: true, Generation: 7}
	tests := []struct {
		name   string
		err    error
		start  contract.GuardSnapshot
		finish contract.GuardSnapshot
		want   contract.ScanOutcome
	}{
		{
			name:  "conclusive success",
			err:   nil,
			start: stable, finish: stable,
			want: contract.ScanSuccess,
		},
		{
			name:  "typed proxy error is conclusive failure",
			err:   &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "connect refused"},
			start: stable, finish: stable,
			want: contract.ScanFailure,
		},
		{
			name:  "wrapped typed proxy error still classified",
			err:   fmt.Errorf("dial 1.2.3.4:8080: %w", &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "timeout"}),
			start: stable, finish: stable,
			want: contract.ScanFailure,
		},
		{
			name:  "target 429 is inconclusive",
			err:   &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 429", RetryAfter: 90 * time.Second},
			start: stable, finish: stable,
			want: contract.ScanInconclusive,
		},
		{
			name:  "unknown error is inconclusive",
			err:   errors.New("boom"),
			start: stable, finish: stable,
			want: contract.ScanInconclusive,
		},
		{
			name:  "parent cancellation is inconclusive",
			err:   context.Canceled,
			start: stable, finish: stable,
			want: contract.ScanInconclusive,
		},
		{
			name:  "wrapped cancellation inside check error is inconclusive",
			err:   &contract.ProxyCheckError{Kind: contract.CheckCancelled, Err: context.Canceled},
			start: stable, finish: stable,
			want: contract.ScanInconclusive,
		},
		{
			name:  "healthy-guard deadline is conclusive failure",
			err:   &contract.ProxyCheckError{Kind: contract.CheckDeadline, Reason: "check timeout"},
			start: stable, finish: stable,
			want: contract.ScanFailure,
		},
		{
			name:   "guard generation change fences failure",
			err:    &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "connect refused"},
			start:  contract.GuardSnapshot{Healthy: true, Generation: 7},
			finish: contract.GuardSnapshot{Healthy: true, Generation: 8},
			want:   contract.ScanInconclusive,
		},
		{
			name:   "guard restart fences failure",
			err:    &contract.ProxyCheckError{Kind: contract.CheckDeadline, Reason: "check timeout"},
			start:  contract.GuardSnapshot{Healthy: true, Generation: 7},
			finish: contract.GuardSnapshot{Healthy: false, Generation: 7},
			want:   contract.ScanInconclusive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyCheck(tt.err, tt.start, tt.finish); got != tt.want {
				t.Fatalf("ClassifyCheck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func scanPolicy() contract.ProxyScanPolicy {
	return contract.ProxyScanPolicy{
		VerifyInterval:    60 * time.Second,
		FreshnessTTL:      120 * time.Second,
		RecoveryBase:      time.Minute,
		RecoveryCap:       15 * time.Minute,
		RecoveryHorizon:   time.Hour,
		LeaseDuration:     30 * time.Second,
		MinRepeatInterval: time.Second,
		InconclusiveRetry: 60 * time.Second,
		DeadAfter:         7 * 24 * time.Hour,
		FailThreshold:     5,
	}
}

func scanRecord(step int) contract.ProxyRecord {
	return contract.ProxyRecord{ID: 42, Protocol: "http", IP: "1.2.3.4", Port: 8080, RecoveryStep: step}
}

func TestMakeScanUpdateSuccessResetsAndCopiesPolicy(t *testing.T) {
	p := scanPolicy()
	rec := scanRecord(7) // old failure history must reset on success

	update := MakeScanUpdate(p, rec, 1234, nil, stableGuard(), stableGuard())

	if update.Outcome != contract.ScanSuccess {
		t.Fatalf("outcome = %v, want success", update.Outcome)
	}
	if update.RecoveryStep != 0 {
		t.Fatalf("recovery step = %d, want 0 after success", update.RecoveryStep)
	}
	if update.NextDelay != p.VerifyInterval {
		t.Fatalf("next delay = %v, want verify interval %v", update.NextDelay, p.VerifyInterval)
	}
	if update.RepeatDelay != p.MinRepeatInterval {
		t.Fatalf("repeat delay = %v, want repeat floor %v", update.RepeatDelay, p.MinRepeatInterval)
	}
	if update.LatencyMS != 1234 {
		t.Fatalf("latency = %d, want 1234", update.LatencyMS)
	}
	if update.FreshnessTTL != p.FreshnessTTL || update.DeadAfter != p.DeadAfter || update.FailThreshold != p.FailThreshold {
		t.Fatalf("policy not copied: ttl=%v dead=%v threshold=%d", update.FreshnessTTL, update.DeadAfter, update.FailThreshold)
	}
}

func TestMakeScanUpdateFailureSchedulesRecoveryNotCooldown(t *testing.T) {
	p := scanPolicy()

	update := MakeScanUpdate(p, scanRecord(0), 0, proxyErr(), stableGuard(), stableGuard())
	if update.Outcome != contract.ScanFailure {
		t.Fatalf("outcome = %v, want failure", update.Outcome)
	}
	if update.NextDelay != p.RecoveryBase {
		t.Fatalf("first recovery delay = %v, want base %v", update.NextDelay, p.RecoveryBase)
	}
	if update.RecoveryStep != 1 {
		t.Fatalf("recovery step = %d, want 1", update.RecoveryStep)
	}
	// The recovery backoff must not become the common background cooldown;
	// only the repeat floor governs the shared repeat position.
	if update.RepeatDelay != p.MinRepeatInterval {
		t.Fatalf("repeat delay = %v, want repeat floor %v", update.RepeatDelay, p.MinRepeatInterval)
	}

	update = MakeScanUpdate(p, scanRecord(1), 0, proxyErr(), stableGuard(), stableGuard())
	if update.NextDelay != 2*p.RecoveryBase {
		t.Fatalf("second recovery delay = %v, want %v", update.NextDelay, 2*p.RecoveryBase)
	}
	if update.RecoveryStep != 2 {
		t.Fatalf("recovery step = %d, want 2", update.RecoveryStep)
	}
	if update.FreshnessTTL != p.FreshnessTTL || update.DeadAfter != p.DeadAfter || update.FailThreshold != p.FailThreshold {
		t.Fatalf("policy not copied: ttl=%v dead=%v threshold=%d", update.FreshnessTTL, update.DeadAfter, update.FailThreshold)
	}
}

func TestMakeScanUpdateCarriesRetirementRuleOnEveryOutcome(t *testing.T) {
	p := scanPolicy()
	p.Retirement = contract.RetirementRule{MinFailures: 10, MinAge: 14 * 24 * time.Hour}

	for name, err := range map[string]error{"success": nil, "failure": proxyErr(), "inconclusive": errors.New("local")} {
		update := MakeScanUpdate(p, scanRecord(0), 0, err, stableGuard(), stableGuard())
		if update.Retirement != p.Retirement {
			t.Errorf("%s: retirement rule = %+v, want %+v", name, update.Retirement, p.Retirement)
		}
	}
}

func TestMakeScanUpdateFailureStepSaturates(t *testing.T) {
	p := scanPolicy()

	update := MakeScanUpdate(p, scanRecord(1<<30), 0, proxyErr(), stableGuard(), stableGuard())
	if update.NextDelay != p.RecoveryCap {
		t.Fatalf("saturated delay = %v, want cap %v", update.NextDelay, p.RecoveryCap)
	}
	if update.RecoveryStep >= 1<<30 {
		t.Fatalf("saturated step = %d, want clamped far below failure history", update.RecoveryStep)
	}
	// Once saturated, further failures neither grow the step nor change the delay.
	again := MakeScanUpdate(p, scanRecord(update.RecoveryStep), 0, proxyErr(), stableGuard(), stableGuard())
	if again.RecoveryStep != update.RecoveryStep {
		t.Fatalf("saturated step grew: %d, want stable %d", again.RecoveryStep, update.RecoveryStep)
	}
	if again.NextDelay != p.RecoveryCap {
		t.Fatalf("saturated delay = %v, want stable cap %v", again.NextDelay, p.RecoveryCap)
	}
}

func TestMakeScanUpdateInconclusivePreservesState(t *testing.T) {
	p := scanPolicy()

	// Inconclusive right after success must not request a failure
	// transition: the step stays at 0 and the outcome stays inconclusive.
	target429 := &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 429", RetryAfter: 90 * time.Second}
	update := MakeScanUpdate(p, scanRecord(0), 0, target429, stableGuard(), stableGuard())
	if update.Outcome != contract.ScanInconclusive {
		t.Fatalf("outcome = %v, want inconclusive", update.Outcome)
	}
	if update.RecoveryStep != 0 {
		t.Fatalf("recovery step = %d, want preserved 0", update.RecoveryStep)
	}
	if update.NextDelay != 90*time.Second || update.RepeatDelay != 90*time.Second {
		t.Fatalf("delays = %v/%v, want Retry-After %v", update.NextDelay, update.RepeatDelay, 90*time.Second)
	}

	// Unknown errors carry no Retry-After; the inconclusive retry floor applies.
	update = MakeScanUpdate(p, scanRecord(3), 0, errors.New("boom"), stableGuard(), stableGuard())
	if update.Outcome != contract.ScanInconclusive {
		t.Fatalf("outcome = %v, want inconclusive", update.Outcome)
	}
	if update.RecoveryStep != 3 {
		t.Fatalf("recovery step = %d, want preserved 3", update.RecoveryStep)
	}
	if update.NextDelay != p.InconclusiveRetry || update.RepeatDelay != p.InconclusiveRetry {
		t.Fatalf("delays = %v/%v, want inconclusive retry %v", update.NextDelay, update.RepeatDelay, p.InconclusiveRetry)
	}

	// A Retry-After shorter than the floor does not shorten the cooldown.
	short429 := &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 429", RetryAfter: 30 * time.Second}
	update = MakeScanUpdate(p, scanRecord(3), 0, short429, stableGuard(), stableGuard())
	if update.NextDelay != p.InconclusiveRetry || update.RepeatDelay != p.InconclusiveRetry {
		t.Fatalf("delays = %v/%v, want inconclusive retry %v", update.NextDelay, update.RepeatDelay, p.InconclusiveRetry)
	}
}

func stableGuard() contract.GuardSnapshot {
	return contract.GuardSnapshot{Healthy: true, Generation: 7}
}

func proxyErr() error {
	return &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "connect refused"}
}
