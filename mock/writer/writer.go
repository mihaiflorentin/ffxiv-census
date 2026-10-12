// Package writer provides an in-memory contract.ResultWriter fake for tests.
// It "commits" every result synchronously: Write records the result and
// returns WriteErr (nil by default), so handler tests need no batching, timers
// or database.
package writer

import (
	"context"
	"sync"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Fake records the results it is given.
type Fake struct {
	mu       sync.Mutex
	results  []contract.CensusResult
	closed   bool
	chars    contract.CharacterRepository
	ach      contract.AchievementRepository
	WriteErr error
}

func NewFake() *Fake { return &Fake{} }

// NewStoring returns a Fake that also applies every committed result to the
// given repositories, like a real writer committing a batch: characters and
// achievement summaries through chars.StoreResult, then the milestones into
// ach (nil to skip them). Handler tests use it to assert on stored state.
func NewStoring(chars contract.CharacterRepository, ach contract.AchievementRepository) *Fake {
	return &Fake{chars: chars, ach: ach}
}

func (f *Fake) Write(ctx context.Context, result contract.CensusResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return contract.ErrWriterClosed
	}
	if f.WriteErr != nil {
		return f.WriteErr
	}
	if f.chars != nil {
		if err := f.chars.StoreResult(ctx, result); err != nil {
			return err
		}
	}
	if f.ach != nil {
		for _, a := range result.Achievements {
			if a.Private {
				continue
			}
			if err := f.ach.UpsertCharacterMilestones(ctx, a.CharacterID, a.Milestones); err != nil {
				return err
			}
		}
	}
	f.results = append(f.results, result)
	return nil
}

func (f *Fake) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// Results returns a copy of the committed results in arrival order.
func (f *Fake) Results() []contract.CensusResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.CensusResult(nil), f.results...)
}

// Characters returns every committed character write, flattened.
func (f *Fake) Characters() []contract.CharacterWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []contract.CharacterWrite
	for _, r := range f.results {
		out = append(out, r.Characters...)
	}
	return out
}

var _ contract.ResultWriter = (*Fake)(nil)
