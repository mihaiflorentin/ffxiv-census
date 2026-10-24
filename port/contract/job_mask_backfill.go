package contract

import "context"

// JobMaskBackfillBatch reports one backfill batch.
type JobMaskBackfillBatch struct {
	// Scanned is how many characters the batch walked past.
	Scanned int
	// Filled is how many of them had NULL job bitmasks and were filled.
	Filled int
	// Cursor is the stored cursor after the batch: the last character ID walked.
	Cursor uint32
	// Done reports that the walk reached the last stored character.
	Done bool
}

// JobMaskBackfillRepository fills the job bitmasks of characters stored
// before the masks existed. It is run by the operator, never at boot.
type JobMaskBackfillRepository interface {
	// BackfillJobMasksBatch walks the next limit characters by ascending ID
	// after the stored cursor, fills the masks that are still NULL from the
	// stored jobs (already-set masks are left alone), and advances the cursor,
	// all in one short transaction. Stopping between batches is always safe:
	// the next call resumes from the cursor.
	BackfillJobMasksBatch(ctx context.Context, limit int) (JobMaskBackfillBatch, error)
}
