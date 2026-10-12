package writer_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func result(ids ...uint32) contract.CensusResult {
	var r contract.CensusResult
	for _, id := range ids {
		r.Characters = append(r.Characters, contract.CharacterWrite{Record: contract.CharacterRecord{ID: id, Name: "c"}})
	}
	return r
}

// writeAsync runs Write on its own goroutine and returns a channel with its outcome.
func writeAsync(w contract.ResultWriter, r contract.CensusResult) <-chan error {
	done := make(chan error, 1)
	go func() { done <- w.Write(context.Background(), r) }()
	return done
}

func waitErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Write did not return")
		return nil
	}
}

type flushRecord struct {
	size int
	err  error
}

type recordingObserver struct {
	mu      sync.Mutex
	flushes []flushRecord
}

func (o *recordingObserver) ObserveFlush(size int, _ time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.flushes = append(o.flushes, flushRecord{size, err})
}

func (o *recordingObserver) all() []flushRecord {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]flushRecord(nil), o.flushes...)
}

func TestBatchWriter_FlushesWhenBatchSizeReachedInOneBatch(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 3, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	a := writeAsync(w, result(1))
	b := writeAsync(w, result(2))
	c := writeAsync(w, result(3))
	for _, ch := range []<-chan error{a, b, c} {
		if err := waitErr(t, ch); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	if repo.UpsertBatchCalls != 1 {
		t.Fatalf("UpsertBatch calls = %d, want 1 (one transaction for the whole batch)", repo.UpsertBatchCalls)
	}
	for _, id := range []uint32{1, 2, 3} {
		if got, _ := repo.Get(context.Background(), id); got == nil {
			t.Fatalf("character %d not stored", id)
		}
	}
}

func TestBatchWriter_WriteDoesNotReturnBeforeFlush(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 100, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	done := writeAsync(w, result(1))
	select {
	case err := <-done:
		t.Fatalf("Write returned %v before the batch flushed", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBatchWriter_FlushesAfterInterval(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 100, FlushInterval: 30 * time.Millisecond}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	if err := waitErr(t, writeAsync(w, result(1))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got, _ := repo.Get(context.Background(), 1); got == nil {
		t.Fatal("character not stored after the interval flush")
	}
}

func TestBatchWriter_FailedCommitIsReturnedToEveryWaiter(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	boom := errors.New("commit failed")
	repo.UpsertBatchErr = boom
	obs := &recordingObserver{}
	w := writer.New(repo, writer.Config{BatchSize: 2, FlushInterval: time.Hour}, obs)
	defer func() { _ = w.Close(context.Background()) }()

	a := writeAsync(w, result(1))
	b := writeAsync(w, result(2))
	for _, ch := range []<-chan error{a, b} {
		if err := waitErr(t, ch); !errors.Is(err, boom) {
			t.Fatalf("Write error = %v, want %v", err, boom)
		}
	}
	if got := obs.all(); len(got) != 1 || got[0].size != 2 || !errors.Is(got[0].err, boom) {
		t.Fatalf("observed flushes = %+v, want one failed flush of size 2", got)
	}
}

func TestBatchWriter_ObservesSuccessfulFlush(t *testing.T) {
	obs := &recordingObserver{}
	w := writer.New(mockrepo.NewCharacterFake(), writer.Config{BatchSize: 2, FlushInterval: time.Hour}, obs)
	defer func() { _ = w.Close(context.Background()) }()

	if err := waitErr(t, writeAsync(w, result(1, 2))); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := obs.all(); len(got) != 1 || got[0].size != 2 || got[0].err != nil {
		t.Fatalf("observed flushes = %+v, want one successful flush of size 2", got)
	}
}

func TestBatchWriter_CloseFlushesPendingResults(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 100, FlushInterval: time.Hour}, nil)

	pending := writeAsync(w, result(1))
	time.Sleep(50 * time.Millisecond) // let the write reach the writer

	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := waitErr(t, pending); err != nil {
		t.Fatalf("pending Write: %v", err)
	}
	if got, _ := repo.Get(context.Background(), 1); got == nil {
		t.Fatal("pending result lost on Close")
	}
}

func TestBatchWriter_WriteAfterCloseIsRejected(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 1, FlushInterval: time.Hour}, nil)
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Write(context.Background(), result(1)); !errors.Is(err, contract.ErrWriterClosed) {
		t.Fatalf("Write after Close = %v, want ErrWriterClosed", err)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestBatchWriter_WriteHonoursContext(t *testing.T) {
	w := writer.New(mockrepo.NewCharacterFake(), writer.Config{BatchSize: 100, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := w.Write(ctx, result(1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Write = %v, want DeadlineExceeded", err)
	}
}

func TestBatchWriter_EmptyResultReturnsImmediately(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	w := writer.New(repo, writer.Config{BatchSize: 100, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	if err := waitErr(t, writeAsync(w, contract.CensusResult{})); err != nil {
		t.Fatalf("Write(empty): %v", err)
	}
	if repo.UpsertBatchCalls != 0 {
		t.Fatalf("UpsertBatch calls = %d, want 0", repo.UpsertBatchCalls)
	}
}
