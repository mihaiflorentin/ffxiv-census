package mockqueue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestFake_RecordsAckedAndRetriedByHandlerOutcome(t *testing.T) {
	q := NewFake()
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "ok"})
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "bad"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, nil, 1, func(_ context.Context, job contract.QueueJob) error {
			if job.Type == "bad" {
				return errors.New("boom")
			}
			return nil
		})
	}()
	waitFor(t, func() bool { return len(q.Acked()) == 1 && len(q.Retried()) == 1 })
	cancel()
	<-done

	if q.Acked()[0].Type != "ok" || q.Retried()[0].Type != "bad" {
		t.Fatalf("acked = %+v, retried = %+v", q.Acked(), q.Retried())
	}
}

func TestFake_ConsumeRunsHandlersConcurrently(t *testing.T) {
	q := NewFake()
	for range 3 {
		_ = q.Publish(context.Background(), contract.QueueJob{Type: "x"})
	}
	var inFlight, peak atomic.Int32
	release := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, nil, 3, func(context.Context, contract.QueueJob) error {
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			return nil
		})
	}()
	waitFor(t, func() bool { return peak.Load() == 3 })
	close(release)
	waitFor(t, func() bool { return len(q.Acked()) == 3 })
	cancel()
	<-done
}

func TestFake_ConsumeWaitsForInFlightHandlersOnCancel(t *testing.T) {
	q := NewFake()
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "x"})
	started := make(chan struct{})
	release := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, nil, 1, func(context.Context, contract.QueueJob) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Consume returned while a handler was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
	if len(q.Acked()) != 1 {
		t.Fatalf("acked = %d, want 1", len(q.Acked()))
	}
}
