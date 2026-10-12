package contract

import (
	"context"
	"errors"
	"time"
)

// ErrWriterClosed is returned by ResultWriter.Write once the writer has been
// closed: the result was not accepted and nothing was stored.
var ErrWriterClosed = errors.New("result writer closed")

// CensusResult is a handler's finished work, ready to be stored. Handlers
// fill only the parts their event produces. New result kinds (milestones,
// latest achievement) are added as fields here so the port stays stable.
type CensusResult struct {
	Characters   []CharacterWrite
	Achievements []AchievementWrite
}

// AchievementWrite is one finished achievement census of a character: its
// earned milestones and the latest-achievement summary, stored together with
// the rest of the batch.
type AchievementWrite struct {
	CharacterID uint32
	// Private marks a profile that hides its achievements. The store only
	// flags the character private and keeps its earlier milestones and latest
	// achievement; Milestones, LatestID and LatestAt are ignored.
	Private    bool
	Milestones []CharacterMilestone
	// LatestID and LatestAt are the character's latest achievement, nil when
	// it has none.
	LatestID *uint32
	LatestAt *time.Time
}

// Size is the number of rows-of-work in the result (characters plus
// achievement updates); a writer counts it towards its batch size.
func (r CensusResult) Size() int { return len(r.Characters) + len(r.Achievements) }

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
