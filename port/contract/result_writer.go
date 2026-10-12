package contract

import (
	"context"
	"errors"
	"time"
)

// ErrWriterClosed is returned by ResultWriter.Write once the writer has been
// closed: the result was not accepted and nothing was stored.
var ErrWriterClosed = errors.New("result writer closed")

// ErrInvalidData marks a store failure caused by the DATA of a write (a
// constraint violation, a value the database rejects) rather than by the
// infrastructure (lost connection, timeout, shutdown). Retrying the same data
// can never succeed, but the other writes of a batch are unaffected, so a
// writer may isolate the offending result instead of failing the whole batch.
// CharacterRepository.UpsertBatch returns it joined with the cause.
var ErrInvalidData = errors.New("invalid data")

// CensusResult is a handler's finished work, ready to be stored. Handlers
// fill only the parts their event produces. New result kinds (milestones,
// latest achievement) are added as fields here so the port stays stable.
type CensusResult struct {
	Characters []CharacterWrite
}

// Size is the number of rows-of-work in the result; a writer counts it
// towards its batch size.
func (r CensusResult) Size() int { return len(r.Characters) }

// ResultWriter stores finished census results in batches. Handlers hand it a
// result and wait: Write returns nil only after the batch containing the
// result has COMMITTED, and returns the commit error otherwise. The worker
// acks a queue job only when its handler returns, so an ack never precedes
// the commit and a failed commit is never acked.
type ResultWriter interface {
	// Write blocks until the result's batch has committed or failed, or ctx
	// is done. A cancelled ctx stops the wait but may not undo a result that
	// was already accepted; stores are idempotent upserts, so a redelivery
	// of the job is safe. After Close it returns ErrWriterClosed.
	Write(ctx context.Context, result CensusResult) error
	// Close stops accepting results, flushes everything pending, and waits
	// for that flush. Safe to call more than once.
	Close(ctx context.Context) error
}

// ResultWriterObserver receives one measurement per batch flush.
type ResultWriterObserver interface {
	// ObserveFlush reports a flushed batch: its size (see CensusResult.Size),
	// how long the commit took, and the commit error (nil on success).
	ObserveFlush(size int, duration time.Duration, err error)
}
