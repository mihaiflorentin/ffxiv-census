package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// defaultRetireBatchPause is the rest between two batches of a sweep. It
// keeps a first pass over millions of rows from competing with the scan
// claims for write throughput and WAL.
const defaultRetireBatchPause = 100 * time.Millisecond

// RetirementSweep retires proxies that never worked and kept failing, in
// bounded batches. The scan worker retires a proxy in the same write as the
// failing scan that crosses the thresholds; the sweep covers what that path
// never revisits: the rows that were already over the thresholds when
// retirement was introduced and rows that only crossed the age threshold with
// time. Several replicas may sweep at once, a retired row is never touched
// twice.
type RetirementSweep struct {
	store    contract.ProxyRetirementStore
	rule     contract.RetirementRule
	interval time.Duration
	batch    int
	pause    time.Duration
	logger   contract.Logger
}

// NewRetirementSweep creates a sweep that runs a full pass every interval, in
// batches of batch rows. A disabled rule makes Run return immediately.
func NewRetirementSweep(store contract.ProxyRetirementStore, rule contract.RetirementRule,
	interval time.Duration, batch int, logger contract.Logger,
) *RetirementSweep {
	return &RetirementSweep{
		store:    store,
		rule:     rule,
		interval: interval,
		batch:    batch,
		pause:    defaultRetireBatchPause,
		logger:   logger,
	}
}

// SweepOnce runs one full pass over the table and returns the number of rows
// retired. It stops early, returning what it retired so far and the error,
// when the context ends or a batch fails; the next pass starts from the
// beginning and skips what is already retired.
func (s *RetirementSweep) SweepOnce(ctx context.Context) (int64, error) {
	var total, after int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.store.RetireStale(ctx, s.rule, after, s.batch)
		if err != nil {
			return total, fmt.Errorf("proxy retirement sweep: %w", err)
		}
		total += res.Retired
		if res.Done {
			return total, nil
		}
		after = res.NextID
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		case <-time.After(s.pause):
		}
	}
}

// Run sweeps once at start and then every interval until the context is
// cancelled, and returns nil on cancellation. A failed pass is logged and
// retried at the next interval; it never stops the scan worker around it.
func (s *RetirementSweep) Run(ctx context.Context) error {
	if !s.rule.Enabled() {
		s.logger.InfoContext(ctx, "proxy_retire.disabled")
		return nil
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		s.pass(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *RetirementSweep) pass(ctx context.Context) {
	started := time.Now()
	retired, err := s.SweepOnce(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "proxy_retire.sweep_error", "retired", retired, "error", err)
		}
		return
	}
	s.logger.InfoContext(ctx, "proxy_retire.sweep",
		"retired", retired, "duration", time.Since(started).Truncate(time.Millisecond))
}
