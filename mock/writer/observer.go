package writer

import (
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Flush is one batch flush as reported to a contract.ResultWriterObserver.
type Flush struct {
	Size     int
	Duration time.Duration
	Err      error
}

// ObserverFake is a contract.ResultWriterObserver that records every flush.
type ObserverFake struct {
	mu      sync.Mutex
	flushes []Flush
}

func NewObserverFake() *ObserverFake { return &ObserverFake{} }

func (o *ObserverFake) ObserveFlush(size int, duration time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.flushes = append(o.flushes, Flush{Size: size, Duration: duration, Err: err})
}

// Flushes returns a copy of the recorded flushes in arrival order.
func (o *ObserverFake) Flushes() []Flush {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Flush(nil), o.flushes...)
}

var _ contract.ResultWriterObserver = (*ObserverFake)(nil)
