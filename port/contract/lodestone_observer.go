package contract

// LodestoneOutcome classifies one Lodestone HTTP request attempt.
type LodestoneOutcome string

const (
	// LodestoneOutcomeSuccess is a usable response (any status below 500
	// that is not a rate limit or challenge, including genuine 403/404).
	LodestoneOutcomeSuccess LodestoneOutcome = "success"
	// LodestoneOutcomeRateLimited is an HTTP 429.
	LodestoneOutcomeRateLimited LodestoneOutcome = "rate_limited"
	// LodestoneOutcomeChallenge is a Cloudflare 202 or 403 interstitial.
	LodestoneOutcomeChallenge LodestoneOutcome = "challenge"
	// LodestoneOutcomeFailure is a transport error, unreadable body or 5xx.
	LodestoneOutcomeFailure LodestoneOutcome = "failure"
)

// LodestoneRequestObserver receives one outcome per Lodestone request
// attempt, tagged with the proxy address the request went through (empty for
// direct requests). Implementations must bound any label derived from proxy.
type LodestoneRequestObserver interface {
	ObserveRequest(proxy string, outcome LodestoneOutcome)
}
