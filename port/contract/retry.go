package contract

import (
	"errors"
	"time"
)

// DefaultRetryTiers returns the delay tiers a retried job waits in: the
// ladder 5 s, 30 s, 5 min, 1 h. The result is a fresh slice.
func DefaultRetryTiers() []time.Duration {
	return []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, time.Hour}
}

// ProxyFaultError marks a handler error as the proxy's fault rather than the
// job's. Handlers and workers wrap an error in it when the failure has no
// *ProxyCheckError of its own (e.g. no proxy could be acquired). Decisions use
// errors.As on this type, never error text.
type ProxyFaultError struct {
	Err error
}

// Error returns a bounded diagnostic string for logs.
func (e *ProxyFaultError) Error() string {
	if e.Err == nil {
		return "proxy fault"
	}
	return "proxy fault: " + e.Err.Error()
}

// Unwrap exposes the wrapped cause so errors.Is/errors.As traverse it.
func (e *ProxyFaultError) Unwrap() error { return e.Err }

// IsProxyAttributable reports whether a handler error is the proxy's fault: a
// proxy dial failure, a destination challenge, 429 or 5xx, or a timeout (a
// *ProxyCheckError of kind CheckProxy, CheckTarget or CheckDeadline), or any
// error wrapped in *ProxyFaultError. Such failures do not count as job
// attempts. Unknown and local errors are the job's.
func IsProxyAttributable(err error) bool {
	if err == nil {
		return false
	}
	var fault *ProxyFaultError
	if errors.As(err, &fault) {
		return true
	}
	var checkErr *ProxyCheckError
	if errors.As(err, &checkErr) {
		switch checkErr.Kind {
		case CheckProxy, CheckTarget, CheckDeadline:
			return true
		}
	}
	return false
}

// RetryDecision is the fate of a failed delivery.
type RetryDecision struct {
	// Dead: park on the event's dead queue instead of retrying.
	Dead bool
	// Attempts is the job-attributable failure count the retried message
	// carries. It is the old count for a proxy-caused failure.
	Attempts int
	// ProxyFault reports that the failure was proxy-attributable and left
	// Attempts unchanged.
	ProxyFault bool
	// Tier is the index into the tier list; Delay is that tier's delay.
	Tier  int
	Delay time.Duration
}

// DecideRetry classifies a handler error. attempts is the number of
// job-attributable failures the delivery already had. Poison payloads and
// no-handler deliveries dead-park immediately. A proxy-attributable error
// keeps the attempt count and retries in the tier of the job's last failure
// (the first tier for a job that never failed on its own). Any other error
// increments the count; reaching maxAttempts dead-parks, otherwise the tier
// is the attempts-th step of the ladder, clamped to the last tier.
func DecideRetry(handlerErr error, attempts, maxAttempts int, tiers []time.Duration) RetryDecision {
	if len(tiers) == 0 {
		tiers = DefaultRetryTiers()
	}
	if errors.Is(handlerErr, ErrPoisonPayload) || errors.Is(handlerErr, ErrNoHandler) {
		return RetryDecision{Dead: true, Attempts: attempts + 1}
	}
	if IsProxyAttributable(handlerErr) {
		tier := tierIndex(attempts, len(tiers))
		return RetryDecision{Attempts: attempts, ProxyFault: true, Tier: tier, Delay: tiers[tier]}
	}
	attempts++
	if maxAttempts > 0 && attempts >= maxAttempts {
		return RetryDecision{Dead: true, Attempts: attempts}
	}
	tier := tierIndex(attempts, len(tiers))
	return RetryDecision{Attempts: attempts, Tier: tier, Delay: tiers[tier]}
}

// tierIndex maps a job-attributable failure count to a tier: count 1 and
// below use the first tier, then one tier per failure, clamped to the last.
func tierIndex(attempts, tiers int) int {
	idx := attempts - 1
	if idx < 0 {
		idx = 0
	}
	if idx > tiers-1 {
		idx = tiers - 1
	}
	return idx
}

// LegacyDrain is the fate of a message found in an old census.<type>.failed
// queue.
type LegacyDrain struct {
	// Dead: the message exhausted its attempts; park it on the dead queue.
	Dead bool
	// Direct: its backoff already elapsed; republish straight to the main
	// exchange.
	Direct bool
	// Tier: otherwise, the delay tier that covers the remaining backoff.
	Tier  int
	Delay time.Duration
}

// DecideLegacyDrain routes a message from the pre-tier failed queue without
// sleeping. attempts is the count it already carries and is not incremented.
// A message with no wait left goes straight to the main exchange; one still
// waiting moves into the smallest tier whose delay covers the remaining wait
// (the longest tier when none does), so it is never retried earlier than the
// old ladder promised.
func DecideLegacyDrain(attempts, maxAttempts int, remaining time.Duration, tiers []time.Duration) LegacyDrain {
	if len(tiers) == 0 {
		tiers = DefaultRetryTiers()
	}
	if maxAttempts > 0 && attempts >= maxAttempts {
		return LegacyDrain{Dead: true}
	}
	if remaining <= 0 {
		return LegacyDrain{Direct: true}
	}
	tier := len(tiers) - 1
	for i, d := range tiers {
		if d >= remaining {
			tier = i
			break
		}
	}
	return LegacyDrain{Tier: tier, Delay: tiers[tier]}
}
