// Package writer is the Postgres-backed batching result writer: a per-process
// adapter for contract.ResultWriter that groups finished census results and
// stores each group in ONE transaction.
package writer

import (
	"context"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const (
	DefaultBatchSize     = 100
	DefaultFlushInterval = time.Second
	DefaultFlushTimeout  = 30 * time.Second
)

// Config tunes the batching. Zero values take the defaults.
type Config struct {
	// BatchSize flushes as soon as this many results (CensusResult.Size)
	// are pending.
	BatchSize int
	// FlushInterval flushes a non-empty batch this long after its first
	// result arrived, however small it still is.
	FlushInterval time.Duration
	// FlushTimeout bounds one batch transaction.
	FlushTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.FlushTimeout <= 0 {
		c.FlushTimeout = DefaultFlushTimeout
	}
	return c
}

type request struct {
	result contract.CensusResult
	done   chan error // buffered(1): the flusher never blocks on a gone caller
}

// BatchWriter implements contract.ResultWriter.
type BatchWriter struct {
	store    contract.CharacterRepository
	cfg      Config
	observer contract.ResultWriterObserver

	in       chan request
	quit     chan struct{}
	stopped  chan struct{}
	quitOnce sync.Once
}

// New starts a writer that stores batches through store. observer may be nil.
// Call Close to flush pending results and stop the background goroutine.
func New(store contract.CharacterRepository, cfg Config, observer contract.ResultWriterObserver) *BatchWriter {
	w := &BatchWriter{
		store:    store,
		cfg:      cfg.withDefaults(),
		observer: observer,
		in:       make(chan request),
		quit:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	go w.run()
	return w
}

// Write hands the result to the batching loop and waits for its batch outcome.
func (w *BatchWriter) Write(ctx context.Context, result contract.CensusResult) error {
	if result.Size() == 0 {
		return nil
	}
	select {
	case <-w.quit:
		return contract.ErrWriterClosed
	default:
	}
	req := request{result: result, done: make(chan error, 1)}
	// in is unbuffered: a successful send means the loop owns the request and
	// will flush it, even if Close races with this call.
	select {
	case w.in <- req:
	case <-w.quit:
		return contract.ErrWriterClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting results, flushes what is pending and waits for it.
func (w *BatchWriter) Close(ctx context.Context) error {
	w.quitOnce.Do(func() { close(w.quit) })
	select {
	case <-w.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *BatchWriter) run() {
	defer close(w.stopped)

	var (
		pending []request
		size    int
		timer   *time.Timer
		timerC  <-chan time.Time
	)
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer, timerC = nil, nil
		}
	}
	flush := func() {
		stopTimer()
		if len(pending) == 0 {
			return
		}
		w.flush(pending, size)
		pending, size = nil, 0
	}

	for {
		select {
		case req := <-w.in:
			if len(pending) == 0 {
				timer = time.NewTimer(w.cfg.FlushInterval)
				timerC = timer.C
			}
			pending = append(pending, req)
			size += req.result.Size()
			if size >= w.cfg.BatchSize {
				flush()
			}
		case <-timerC:
			flush()
		case <-w.quit:
			flush()
			return
		}
	}
}

// flush stores every pending result in one transaction and gives every waiter
// the same outcome.
func (w *BatchWriter) flush(batch []request, size int) {
	var merged contract.CensusResult
	for _, r := range batch {
		merged.Characters = append(merged.Characters, r.result.Characters...)
		merged.Achievements = append(merged.Achievements, r.result.Achievements...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.FlushTimeout)
	defer cancel()
	start := time.Now()
	err := w.store.StoreResult(ctx, merged)
	if w.observer != nil {
		w.observer.ObserveFlush(size, time.Since(start), err)
	}
	for _, r := range batch {
		r.done <- err
	}
}

var _ contract.ResultWriter = (*BatchWriter)(nil)
