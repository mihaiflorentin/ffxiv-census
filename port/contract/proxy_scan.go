package contract

import (
	"context"
	"time"
)

// This file is the single definition site for the proxy scan pipeline
// contract: typed check outcomes, scan value types, and the store/guard
// ports consumed by the scan dispatcher and its persistence adapters.

// ProxyCheckKind attributes a failed proxy check to a cause. Consumers
// classify outcomes from the kind, never from error text.
type ProxyCheckKind uint8

const (
	CheckLocal    ProxyCheckKind = iota // fail closed on unknown/local errors
	CheckProxy                          // proven proxy dial/negotiation failure
	CheckTarget                         // HTTP/payload/target TLS error
	CheckDeadline                       // attribution depends on endpoint guard
	CheckCancelled
)

// String returns a bounded diagnostic label for the kind.
func (k ProxyCheckKind) String() string {
	switch k {
	case CheckLocal:
		return "local"
	case CheckProxy:
		return "proxy"
	case CheckTarget:
		return "target"
	case CheckDeadline:
		return "deadline"
	case CheckCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// ProxyCheckError is the typed result of a failed proxy check. Reason is a
// bounded diagnostic label for logs only; it is never parsed for decisions.
// A nil *ProxyCheckError never means success — check the error value itself.
type ProxyCheckError struct {
	Kind   ProxyCheckKind
	Reason string
	// Challenge marks a Cloudflare challenge served to the delivery's
	// identity (HTTP 202 interstitial or a 403 block page). Challenges are
	// destination-side. Diagnostic only: they flow the queue's standard
	// retry ladder while the worker cools the proxy's destination down.
	Challenge  bool
	RetryAfter time.Duration
	Err        error
}

// Error returns a bounded diagnostic string for logs. Decision code must use
// errors.As/errors.Is on the typed fields instead.
func (e *ProxyCheckError) Error() string {
	switch {
	case e.Reason != "" && e.Err != nil:
		return "proxy check " + e.Kind.String() + ": " + e.Reason + ": " + e.Err.Error()
	case e.Reason != "":
		return "proxy check " + e.Kind.String() + ": " + e.Reason
	case e.Err != nil:
		return "proxy check " + e.Kind.String() + ": " + e.Err.Error()
	default:
		return "proxy check " + e.Kind.String()
	}
}

// Unwrap exposes the wrapped cause so errors.Is/errors.As traverse it.
func (e *ProxyCheckError) Unwrap() error { return e.Err }

// ScanQueue identifies the scheduling queue a scan claim belongs to.
type ScanQueue uint8

const (
	ScanVerification ScanQueue = iota
	ScanRecovery
	ScanBackground
)

// ScanOutcome is the conclusive classification of one completed attempt.
type ScanOutcome uint8

const (
	ScanInconclusive ScanOutcome = iota
	ScanSuccess
	ScanFailure
)

// ProxyScanPolicy holds the scheduler settings shared by domain policy and
// persistence. Storage converts the relative durations to absolute
// timestamps from database time; callers never pass wall-clock deadlines.
type ProxyScanPolicy struct {
	VerifyInterval, FreshnessTTL                        time.Duration
	RecoveryBase, RecoveryCap, RecoveryHorizon          time.Duration
	LeaseDuration, MinRepeatInterval, InconclusiveRetry time.Duration
	DeadAfter                                           time.Duration
	FailThreshold                                       int
	Retirement                                          RetirementRule
}

// RetirementRule decides which proxies leave the background scan for good.
// A proxy is retired when it never worked (no last-alive time), has failed at
// least MinFailures times and was first seen at least MinAge ago. The zero
// value disables retirement.
type RetirementRule struct {
	MinFailures int
	MinAge      time.Duration
}

// Enabled reports whether the rule can retire anything.
func (r RetirementRule) Enabled() bool { return r.MinFailures > 0 }

// ScanLease is the fenced claim a dispatcher holds for one scan attempt.
// Token and Version must match at completion; an unexpired lease is required.
type ScanLease struct {
	Record  ProxyRecord
	Token   string
	Version int64
}

// ScanUpdate is the relative result of one attempt. NextDelay schedules the
// next verification/recovery attempt, RepeatDelay is the common minimum
// repeat/inconclusive cooldown, and RecoveryStep carries the recovery
// progression. FailThreshold and the TTLs are policy copies the store applies.
type ScanUpdate struct {
	Outcome      ScanOutcome
	LatencyMS    int
	NextDelay    time.Duration
	RepeatDelay  time.Duration
	RecoveryStep int

	FreshnessTTL  time.Duration
	DeadAfter     time.Duration
	FailThreshold int
	// Retirement is applied to conclusive failures: the failing attempt that
	// satisfies the rule retires the proxy in the same write.
	Retirement RetirementRule
}

// GuardSnapshot is one consistent endpoint-guard observation. A guard
// generation change between check start and finish fences conclusive
// failure attribution.
type GuardSnapshot struct {
	Healthy    bool
	Generation uint64
}

// ProxyEndpointGuard tracks local endpoint health so deadline failures can be
// attributed to the replica rather than the proxy.
type ProxyEndpointGuard interface {
	// Snapshot returns one consistent guard observation under one lock.
	Snapshot() GuardSnapshot
	// Run maintains the guard state until ctx is cancelled.
	Run(ctx context.Context) error
}

// RetireBatch is the result of one bounded retirement sweep step. NextID is
// the cursor for the following step (the highest row ID examined); Done
// reports that the step found no rows past the cursor, so a full pass is over.
type RetireBatch struct {
	NextID  int64
	Retired int64
	Done    bool
}

// ProxyRetirementStore retires proxies that match a RetirementRule in
// bounded, resumable steps, so the first pass over a very large table never
// holds a long transaction or a table lock.
type ProxyRetirementStore interface {
	// RetireStale examines at most batch rows with an ID above afterID in ID
	// order, retires the ones matching the rule and returns the cursor for the
	// next step. Already retired rows are left untouched, so a sweep is safe
	// to repeat and to run from several replicas at once.
	RetireStale(ctx context.Context, rule RetirementRule, afterID int64, batch int) (RetireBatch, error)
}

// ProxyScanStore leases scan work and persists fenced scan observations.
// CompleteScan returns (false, nil) for fenced/discarded observations and a
// nonnil error for persistence failure; only (true, nil) is an accepted
// completion. A failed database write never fabricates a completed attempt.
type ProxyScanStore interface {
	ClaimScans(ctx context.Context, queue ScanQueue, limit int) ([]ScanLease, error)
	CompleteScan(ctx context.Context, lease ScanLease, update ScanUpdate) (bool, error)
	ReleaseScan(ctx context.Context, lease ScanLease) error
}
