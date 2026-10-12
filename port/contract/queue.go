package contract

import (
	"context"
	"errors"
)

// QueueJob is a unit of async work carried between publishers and consumers.
// Payload is opaque JSON. Type is the event type used for routing.
type QueueJob struct {
	Type    string
	Payload []byte
}

// ErrPoisonPayload marks a delivery whose payload can never be processed:
// undecodable JSON or a semantically invalid job (e.g. an inverted id-sweep
// range). Handlers return it joined with the underlying cause
// (errors.Join(ErrPoisonPayload, err)). The queue dead-parks such
// deliveries immediately instead of retrying — retrying a malformed
// payload can never succeed.
var ErrPoisonPayload = errors.New("poison payload")

// ErrNoHandler marks a delivery whose event type has no registered handler
// in this process (e.g. a failed-queue redelivery reaching a consumer that
// does not implement the event). The queue dead-parks such deliveries
// instead of retrying or silently dropping them.
var ErrNoHandler = errors.New("no handler registered")

// Queue defines a durable work queue with push-based consumption.
// The adapter handles delay-tier retry and dead-letter routing internally — callers only see Publish and Consume.
type Queue interface {
	// Publish sends a single job to the queue. The job's Type is used as the
	// routing key. Returns error on failure.
	Publish(ctx context.Context, job QueueJob) error
	// Consume starts concurrency consumers for the given event types. Each
	// message is dispatched to handler. On handler return:
	//   - nil → message is acked
	//   - ErrPoisonPayload or ErrNoHandler in the chain → message is
	//     dead-parked on the event's dead queue (never dropped, never retried)
	//   - any other error → the message waits in a delay tier (see
	//     DecideRetry: 5s, 30s, 5m, 1h by default) and is redelivered when
	//     the tier delay is over. Only job-attributable failures count
	//     toward the tier and the attempt cap; an error for which
	//     IsProxyAttributable is true leaves the count unchanged. Only the
	//     handler itself can end the retry by acking a terminal outcome
	//     (genuine Lodestone 404/403), or the attempt cap dead-parks it.
	// Consume blocks until ctx is cancelled.
	Consume(ctx context.Context, eventTypes []string, concurrency int, handler func(ctx context.Context, job QueueJob) error) error
	// ConsumeFailed drains the legacy per-event-type failed queues, where
	// retries waited before the delay tiers existed. It never sleeps: each
	// message goes to the main queue under its canonical event type when its
	// old backoff has elapsed, into the delay tier covering the remaining
	// wait otherwise, and is acked only after the broker confirmed the move.
	// Messages whose attempt count has reached maxAttempts, or whose event
	// type is unknown, are dead-parked for inspection — never discarded.
	// If eventTypes is empty, consumes from all failed queues.
	ConsumeFailed(ctx context.Context, eventTypes []string, concurrency int) error
	// Close closes the underlying connection.
	Close() error
}
