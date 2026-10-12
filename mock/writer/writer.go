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
	WriteErr error
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Write(_ context.Context, result contract.CensusResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return contract.ErrWriterClosed
	}
	if f.WriteErr != nil {
		return f.WriteErr
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
