package proxy

import "time"

// maxBackoffDoublings bounds the exponent so a runaway counter can never
// overflow time.Duration before the cap is applied.
const maxBackoffDoublings = 20

// DestinationBackoff returns the destination cooldown for a proxy that has
// already failed `failures` consecutive times: floor * 2^failures, saturated
// at cap. A cap at or below the floor disables growth. A server Retry-After
// hint is honored even above the cap, because the destination asked for it.
func DestinationBackoff(floor, cap, hint time.Duration, failures int) time.Duration {
	delay := floor
	if cap > floor {
		failures = min(max(failures, 0), maxBackoffDoublings)
		for range failures {
			delay *= 2
			if delay >= cap {
				delay = cap
				break
			}
		}
	}
	return max(delay, hint)
}
