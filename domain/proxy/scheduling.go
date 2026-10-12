package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// RecoveryDelay returns the wait before the step-th (0-indexed) additional
// recovery retry: the base interval doubled per step, saturating at cap. The
// computation performs no unbounded shifts or loops and cannot overflow.
func RecoveryDelay(step int, base, cap time.Duration) time.Duration {
	d := base
	for i := 0; i < step && d < cap; i++ {
		if d > cap/2 {
			return cap
		}
		d *= 2
	}
	if d > cap {
		return cap
	}
	return d
}

// ClassifyCheck attributes one completed attempt. A nil error is conclusive
// success. Parent cancellation and untyped (unknown/local) errors are
// inconclusive and never increment failure history. A typed proxy or
// healthy-guard deadline failure counts as conclusive only while the endpoint
// guard was healthy and unchanged across the attempt; guard restarts fence
// all failure attribution. Target errors (429, 5xx, payload, target TLS) are
// always inconclusive. Decisions use errors.As/errors.Is on typed values,
// never error text.
func ClassifyCheck(err error, start, finish contract.GuardSnapshot) contract.ScanOutcome {
	if err == nil {
		return contract.ScanSuccess
	}
	if errors.Is(err, context.Canceled) {
		return contract.ScanInconclusive
	}
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) {
		return contract.ScanInconclusive
	}
	stable := start.Healthy && finish.Healthy && start.Generation == finish.Generation
	if stable && (checkErr.Kind == contract.CheckProxy || checkErr.Kind == contract.CheckDeadline) {
		return contract.ScanFailure
	}
	return contract.ScanInconclusive
}

// MakeScanUpdate translates one completed attempt into the relative update
// the store applies. Success schedules the next verification and resets the
// recovery sequence. Failure schedules the next recovery delay and the
// saturated next step; the repeat position moves by the repeat floor only,
// so recovery backoff never becomes the common background cooldown.
// Inconclusive attempts preserve the step and schedule no sooner than the
// inconclusive retry floor, honoring a longer valid Retry-After. All delays
// are durations: the store computes absolute timestamps from database time.
func MakeScanUpdate(p contract.ProxyScanPolicy, rec contract.ProxyRecord,
	latency int, err error, start, finish contract.GuardSnapshot,
) contract.ScanUpdate {
	update := contract.ScanUpdate{
		LatencyMS:     latency,
		FreshnessTTL:  p.FreshnessTTL,
		DeadAfter:     p.DeadAfter,
		FailThreshold: p.FailThreshold,
		Retirement:    p.Retirement,
	}

	switch outcome := ClassifyCheck(err, start, finish); outcome {
	case contract.ScanSuccess:
		update.Outcome = contract.ScanSuccess
		update.NextDelay = p.VerifyInterval
		update.RepeatDelay = p.MinRepeatInterval
		update.RecoveryStep = 0
	case contract.ScanFailure:
		failure := MakeConsumerFailureUpdate(p, rec)
		update.Outcome = failure.Outcome
		update.NextDelay = failure.NextDelay
		update.RepeatDelay = failure.RepeatDelay
		update.RecoveryStep = failure.RecoveryStep
	default:
		update.Outcome = contract.ScanInconclusive
		update.RecoveryStep = rec.RecoveryStep
		delay := p.InconclusiveRetry
		var checkErr *contract.ProxyCheckError
		if errors.As(err, &checkErr) && checkErr.RetryAfter > delay {
			delay = checkErr.RetryAfter
		}
		update.NextDelay = delay
		update.RepeatDelay = delay
	}
	return update
}

// MakeConsumerFailureUpdate builds the relative failure update for one
// consumer-observed conclusive proxy failure. It mirrors the scanner's
// failure transition: the next recovery delay advances with the failed
// history and the repeat position moves by the common floor only, so a
// consumer failure never widens the background repeat cooldown.
func MakeConsumerFailureUpdate(p contract.ProxyScanPolicy, rec contract.ProxyRecord) contract.ScanUpdate {
	step := rec.RecoveryStep + 1
	if bound := recoveryStepBound(p.RecoveryBase, p.RecoveryCap); step < 1 || step > bound {
		step = bound
	}
	return contract.ScanUpdate{
		Outcome:       contract.ScanFailure,
		FreshnessTTL:  p.FreshnessTTL,
		DeadAfter:     p.DeadAfter,
		FailThreshold: p.FailThreshold,
		NextDelay:     RecoveryDelay(step-1, p.RecoveryBase, p.RecoveryCap),
		RepeatDelay:   p.MinRepeatInterval,
		RecoveryStep:  step,
	}
}

// recoveryStepBound is the largest stored recovery step that can still
// change the next-failure delay: one past the first step whose delay already
// equals cap. Clamping to it keeps repeated failures from growing the stored
// step without bound while the capped delay keeps applying.
func recoveryStepBound(base, cap time.Duration) int {
	s := 1
	for s < 64 && RecoveryDelay(s, base, cap) < cap {
		s++
	}
	return s + 1
}
