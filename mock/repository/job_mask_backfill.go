package repository

import (
	"context"
	"sync"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var _ contract.JobMaskBackfillRepository = (*JobMaskBackfillRepository)(nil)

// JobMaskBackfillRepository is a scripted fake: each call returns the next
// batch, and once the script runs out it reports done with nothing scanned.
type JobMaskBackfillRepository struct {
	mu      sync.Mutex
	Batches []contract.JobMaskBackfillBatch
	Err     error
	// Limits records the batch size of every call.
	Limits []int
}

func NewJobMaskBackfillFake(batches ...contract.JobMaskBackfillBatch) *JobMaskBackfillRepository {
	return &JobMaskBackfillRepository{Batches: batches}
}

func (f *JobMaskBackfillRepository) BackfillJobMasksBatch(_ context.Context, limit int) (contract.JobMaskBackfillBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Limits = append(f.Limits, limit)
	if f.Err != nil {
		return contract.JobMaskBackfillBatch{}, f.Err
	}
	if len(f.Batches) == 0 {
		return contract.JobMaskBackfillBatch{Done: true}, nil
	}
	b := f.Batches[0]
	f.Batches = f.Batches[1:]
	return b, nil
}
