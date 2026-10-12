package writer_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// runSweepJobs consumes one single-ID id-sweep job per id through the real
// ID sweep handler and a real batching writer, with all jobs sharing a batch.
func runSweepJobs(t *testing.T, repo *mockrepo.CharacterRepository, ids ...uint32) *mockqueue.Fake {
	t.Helper()
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}
	svc := census.NewService(repo, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	w := writer.New(repo, writer.Config{BatchSize: len(ids), FlushInterval: 50 * time.Millisecond}, nil)
	h := handler.NewIDSweep(ls, svc, nil).WithWriter(w)

	q := mockqueue.NewFake()
	for _, id := range ids {
		payload, _ := json.Marshal(handler.IDSweepPayload{From: id, To: id})
		if err := q.Publish(context.Background(), contract.QueueJob{Type: "id-sweep", Payload: payload}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- q.Consume(ctx, nil, len(ids), func(ctx context.Context, job contract.QueueJob) error {
			_, err := h.Handle(ctx, job.Payload)
			return err
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(q.Acked())+len(q.Retried()) < len(ids) {
		if time.Now().After(deadline) {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return q
}

func TestWorker_FailedCommitLeavesEveryJobOfTheBatchUnacked(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.UpsertBatchErr = errors.New("connection reset")

	q := runSweepJobs(t, repo, 1, 2, 3)

	if n := len(q.Acked()); n != 0 {
		t.Fatalf("%d jobs acked although their batch never committed", n)
	}
	if n := len(q.Retried()); n != 3 {
		t.Fatalf("%d jobs retried, want all 3 of the batch", n)
	}
	for _, id := range []uint32{1, 2, 3} {
		if stored(t, repo, id) {
			t.Fatalf("character %d stored by a failed commit", id)
		}
	}
}

func TestWorker_OneInvalidResultDoesNotStopTheOthersInItsBatch(t *testing.T) {
	repo := mockrepo.NewCharacterFake()
	repo.RejectID(2)

	q := runSweepJobs(t, repo, 1, 2, 3)

	if n := len(q.Acked()); n != 2 {
		t.Fatalf("%d jobs acked, want the 2 valid ones", n)
	}
	retried := q.Retried()
	if len(retried) != 1 {
		t.Fatalf("%d jobs retried, want only the invalid one", len(retried))
	}
	var p handler.IDSweepPayload
	_ = json.Unmarshal(retried[0].Payload, &p)
	if p.From != 2 {
		t.Fatalf("retried job = %+v, want the one for character 2", p)
	}
	if !stored(t, repo, 1) || !stored(t, repo, 3) || stored(t, repo, 2) {
		t.Fatal("expected characters 1 and 3 stored and 2 not stored")
	}
}
