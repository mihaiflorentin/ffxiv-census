package metrics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestResultWriterObserver(t *testing.T) {
	registry := NewRegistry()
	observer := NewResultWriterObserver(registry)
	observer.ObserveFlush(12, 40*time.Millisecond, nil)
	observer.ObserveFlush(3, 10*time.Millisecond, errors.New("boom"))

	out := registry.Gather()
	for _, want := range []string{
		`result_writer_batch_size_count 2`,
		`result_writer_batch_size_sum 15`,
		`result_writer_flush_duration_seconds_count 2`,
		`result_writer_flush_failures_total 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q:\n%s", want, out)
		}
	}
}
