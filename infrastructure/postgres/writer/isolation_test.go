package writer_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func stored(t *testing.T, repo *mockrepo.CharacterRepository, id uint32) bool {
	t.Helper()
	got, _ := repo.Get(context.Background(), id)
	return got != nil
}

func TestBatchWriter_InvalidResultDoesNotStopItsNeighbours(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(2)
	obs := &recordingObserver{}
	w := writer.New(repo, writer.Config{BatchSize: 3, FlushInterval: time.Hour}, obs)
	defer func() { _ = w.Close(context.Background()) }()

	a := writeAsync(w, result(1))
	b := writeAsync(w, result(2))
	c := writeAsync(w, result(3))

	if err := waitErr(t, a); err != nil {
		t.Fatalf("neighbour 1: %v", err)
	}
	if err := waitErr(t, c); err != nil {
		t.Fatalf("neighbour 3: %v", err)
	}
	if err := waitErr(t, b); !errors.Is(err, contract.ErrInvalidData) {
		t.Fatalf("offending result error = %v, want ErrInvalidData", err)
	}
	if !stored(t, repo, 1) || !stored(t, repo, 3) {
		t.Fatal("valid neighbours were not stored")
	}
	if stored(t, repo, 2) {
		t.Fatal("invalid result was stored")
	}
}

func TestBatchWriter_InvalidCharacterFailsOnlyItsOwnResult(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(11)
	w := writer.New(repo, writer.Config{BatchSize: 4, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	good := writeAsync(w, result(1, 2))
	bad := writeAsync(w, result(10, 11)) // one bad character fails the whole result
	if err := waitErr(t, good); err != nil {
		t.Fatalf("good result: %v", err)
	}
	if err := waitErr(t, bad); !errors.Is(err, contract.ErrInvalidData) {
		t.Fatalf("bad result error = %v, want ErrInvalidData", err)
	}
	if stored(t, repo, 10) {
		t.Fatal("part of the bad result was stored; a result commits atomically")
	}
}

func TestBatchWriter_IsolationBisectsInsteadOfRetryingEveryResult(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(5)
	const n = 16
	w := writer.New(repo, writer.Config{BatchSize: n, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	var chans []<-chan error
	for i := uint32(1); i <= n; i++ {
		chans = append(chans, writeAsync(w, result(i)))
	}
	failed := 0
	for _, ch := range chans {
		if err := waitErr(t, ch); err != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d results failed, want exactly 1", failed)
	}
	// 1 full attempt + 2 per bisection level (4 levels) = 9 at most.
	if repo.UpsertBatchCalls > 9 {
		t.Fatalf("UpsertBatch calls = %d, want <= 9 (bisection, not N retries)", repo.UpsertBatchCalls)
	}
}

func TestBatchWriter_TransientErrorFailsEveryWaiterWithoutRetries(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	boom := fmt.Errorf("connection reset") // not ErrInvalidData: infrastructure failure
	repo.UpsertBatchErr = boom
	w := writer.New(repo, writer.Config{BatchSize: 4, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	var chans []<-chan error
	for i := uint32(1); i <= 4; i++ {
		chans = append(chans, writeAsync(w, result(i)))
	}
	for _, ch := range chans {
		if err := waitErr(t, ch); !errors.Is(err, boom) {
			t.Fatalf("Write error = %v, want %v", err, boom)
		}
	}
	if repo.UpsertBatchCalls != 1 {
		t.Fatalf("UpsertBatch calls = %d, want 1 (no isolation for transient errors)", repo.UpsertBatchCalls)
	}
}

func TestBatchWriter_TransientErrorDuringIsolationStopsFurtherAttempts(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(1)
	w := writer.New(repo, writer.Config{BatchSize: 4, FlushInterval: time.Hour}, nil)
	defer func() { _ = w.Close(context.Background()) }()

	boom := errors.New("connection reset")
	repo.OnUpsertBatch = func(call int) error {
		if call == 2 { // the first isolation attempt hits an outage
			return boom
		}
		return nil
	}
	var chans []<-chan error
	for i := uint32(1); i <= 4; i++ {
		chans = append(chans, writeAsync(w, result(i)))
	}
	for i, ch := range chans {
		if err := waitErr(t, ch); err == nil {
			t.Fatalf("result %d succeeded although the database failed during isolation", i+1)
		}
	}
	if repo.UpsertBatchCalls != 2 {
		t.Fatalf("UpsertBatch calls = %d, want 2 (full batch, then the failing half; no hammering)", repo.UpsertBatchCalls)
	}
}

func TestBatchWriter_ObservesEveryAttemptDuringIsolation(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(2)
	obs := &recordingObserver{}
	w := writer.New(repo, writer.Config{BatchSize: 2, FlushInterval: time.Hour}, obs)
	defer func() { _ = w.Close(context.Background()) }()

	a := writeAsync(w, result(1))
	b := writeAsync(w, result(2))
	_ = waitErr(t, a)
	_ = waitErr(t, b)

	got := obs.all()
	if len(got) != 3 || got[0].size != 2 || got[0].err == nil {
		t.Fatalf("observed = %+v, want failed 2, then two single attempts", got)
	}
}
