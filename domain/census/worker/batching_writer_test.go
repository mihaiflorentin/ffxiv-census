package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census/worker"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/postgrestest"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// gatedRepo wraps the real repository so a test can hold, fail and count the
// batch commits that the writer issues.
type gatedRepo struct {
	contract.CharacterRepository
	gate    chan struct{} // when non-nil, UpsertBatch waits for it to close
	failErr error
	calls   atomic.Int32
}

func (r *gatedRepo) UpsertBatch(ctx context.Context, writes []contract.CharacterWrite) error {
	r.calls.Add(1)
	if r.gate != nil {
		<-r.gate
	}
	if r.failErr != nil {
		return r.failErr
	}
	return r.CharacterRepository.UpsertBatch(ctx, writes)
}

// sweepRig is a worker consuming id-sweep jobs from the in-memory queue,
// fetching from a fake Lodestone and storing through the batching writer into
// a temporary real Postgres.
type sweepRig struct {
	queue  *mockqueue.Fake
	repo   contract.CharacterRepository
	gated  *gatedRepo
	writer *writer.BatchWriter
	run    func() // starts the worker
	stop   func() // cancels the worker and waits for it to return
}

func newSweepRig(t *testing.T, cfg writer.Config, concurrency int) *sweepRig {
	t.Helper()
	real := repository.NewCharacterRepository(postgrestest.NewDriver(t))
	gated := &gatedRepo{CharacterRepository: real}
	w := writer.New(gated, cfg, nil)

	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur",
			ClassJobs: []contract.ClassJobRecord{{ClassJobID: 19, Name: "Paladin", Level: 100}},
		}, nil
	}
	svc := census.NewService(gated, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	reg := handler.NewRegistry()
	reg.Register(handler.EventIDSweep, handler.NewIDSweep(ls, svc, nil).WithWriter(w))

	q := mockqueue.NewFake()
	wk := worker.New(q, reg, nil)

	rig := &sweepRig{queue: q, repo: real, gated: gated, writer: w}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	rig.run = func() {
		go func() {
			defer close(done)
			_ = wk.RunEvents(ctx, []string{handler.EventIDSweep}, concurrency)
		}()
	}
	rig.stop = func() { cancel(); <-done }
	t.Cleanup(func() {
		cancel()
		_ = w.Close(context.Background())
	})
	return rig
}

func (r *sweepRig) publishSweeps(t *testing.T, ids ...uint32) {
	t.Helper()
	for _, id := range ids {
		b, _ := json.Marshal(handler.IDSweepPayload{From: id, To: id})
		if err := r.queue.Publish(context.Background(), contract.QueueJob{Type: handler.EventIDSweep, Payload: b}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stored(t *testing.T, repo contract.CharacterRepository, id uint32) bool {
	t.Helper()
	got, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return got != nil
}

func TestBatchingWriter_JobIsAckedOnlyAfterItsBatchCommits(t *testing.T) {
	rig := newSweepRig(t, writer.Config{BatchSize: 1, FlushInterval: time.Hour}, 1)
	rig.gated.gate = make(chan struct{})
	rig.publishSweeps(t, 101)
	rig.run()
	defer rig.stop()

	eventually(t, "the commit to start", func() bool { return rig.gated.calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("job acked (%d) while its batch was still committing", n)
	}

	close(rig.gated.gate)
	eventually(t, "the job to be acked", func() bool { return len(rig.queue.Acked()) == 1 })
	if !stored(t, rig.repo, 101) {
		t.Fatal("job acked but its character is not stored")
	}
}

func TestBatchingWriter_FailedCommitLeavesJobUnacked(t *testing.T) {
	rig := newSweepRig(t, writer.Config{BatchSize: 1, FlushInterval: time.Hour}, 1)
	rig.gated.failErr = errors.New("commit failed")
	rig.publishSweeps(t, 201)
	rig.run()
	defer rig.stop()

	eventually(t, "the job to be returned for retry", func() bool { return len(rig.queue.Retried()) == 1 })
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("acked %d jobs whose batch failed to commit", n)
	}
	if stored(t, rig.repo, 201) {
		t.Fatal("character stored although the batch failed")
	}
}

func TestBatchingWriter_ManyJobsAreStoredInOneBatch(t *testing.T) {
	const jobs = 20
	rig := newSweepRig(t, writer.Config{BatchSize: jobs, FlushInterval: time.Hour}, jobs)
	var ids []uint32
	for i := uint32(1); i <= jobs; i++ {
		ids = append(ids, 300+i)
	}
	rig.publishSweeps(t, ids...)
	rig.run()
	defer rig.stop()

	eventually(t, "every job to be acked", func() bool { return len(rig.queue.Acked()) == jobs })
	for _, id := range ids {
		if !stored(t, rig.repo, id) {
			t.Fatalf("character %d missing", id)
		}
	}
	if calls := rig.gated.calls.Load(); calls != 1 {
		t.Fatalf("batch commits = %d, want 1 for %d jobs", calls, jobs)
	}
}

func TestBatchingWriter_ShutdownFlushesPendingResults(t *testing.T) {
	rig := newSweepRig(t, writer.Config{BatchSize: 1000, FlushInterval: time.Hour}, 3)
	rig.publishSweeps(t, 401, 402, 403)
	rig.run()

	// All three handlers are now waiting inside the writer: nothing flushes by
	// size or interval.
	time.Sleep(200 * time.Millisecond)
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("acked %d jobs before any flush", n)
	}
	if rig.gated.calls.Load() != 0 {
		t.Fatal("batch flushed before shutdown")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rig.stop() // returns once the in-flight handlers are released
	}()
	if err := rig.writer.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()

	if n := len(rig.queue.Acked()); n != 3 {
		t.Fatalf("acked = %d, want 3 after the shutdown flush", n)
	}
	for _, id := range []uint32{401, 402, 403} {
		if !stored(t, rig.repo, id) {
			t.Fatalf("character %d lost at shutdown", id)
		}
	}
}
