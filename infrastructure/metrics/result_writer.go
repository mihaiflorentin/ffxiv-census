package metrics

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ResultWriterObserver publishes batching-writer health: batch size, flush
// latency and flush failures.
type ResultWriterObserver struct {
	batchSize     *Histogram
	flushDuration *Histogram
	flushFailures *Counter
}

func NewResultWriterObserver(registry *Registry) *ResultWriterObserver {
	return &ResultWriterObserver{
		batchSize:     registry.NewHistogram("result_writer_batch_size", "Results stored per batch flush", []float64{1, 5, 10, 25, 50, 100, 250, 500}),
		flushDuration: registry.NewHistogram("result_writer_flush_duration_seconds", "Batch flush (single transaction) duration in seconds", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}),
		flushFailures: registry.NewCounter("result_writer_flush_failures_total", "Batch flushes that failed to commit"),
	}
}

func (o *ResultWriterObserver) ObserveFlush(size int, duration time.Duration, err error) {
	o.batchSize.Observe(nil, float64(size))
	o.flushDuration.Observe(nil, duration.Seconds())
	if err != nil {
		o.flushFailures.Inc(nil)
	}
}

var _ contract.ResultWriterObserver = (*ResultWriterObserver)(nil)
