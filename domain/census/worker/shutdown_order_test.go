package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
)

// The production shutdown order: SIGTERM stops claiming, in-flight handlers
// keep waiting on their batch, the interval flush commits it, the worker
// returns, and only then is the writer closed. No writer Close is needed for
// the in-flight job to be stored and acked.
func TestBatchingWriter_ShutdownLetsInFlightHandlersFinishBeforeWriterCloses(t *testing.T) {
	rig := newSweepRig(t, writer.Config{BatchSize: 1000, FlushInterval: 300 * time.Millisecond}, 2)
	rig.publishSweeps(t, 501, 502)
	rig.run()

	// Both handlers are waiting inside the writer for the interval flush.
	eventually(t, "handlers to be waiting on the writer", func() bool {
		return len(rig.queue.Acked()) == 0 && rig.gated.calls.Load() == 0
	})

	returned := make(chan struct{})
	go func() {
		rig.stop() // cancel claiming (SIGTERM) and wait for the worker to return
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not return: in-flight handlers were not allowed to finish")
	}

	if n := len(rig.queue.Acked()); n != 2 {
		t.Fatalf("acked = %d after worker returned, want 2", n)
	}
	for _, id := range []uint32{501, 502} {
		if !stored(t, rig.repo, id) {
			t.Fatalf("character %d lost at shutdown", id)
		}
	}
	if err := rig.writer.Close(context.Background()); err != nil {
		t.Fatalf("Close after worker stop: %v", err)
	}
}
