package worker_test

import (
	"context"
	"encoding/json"
	"errors"
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

// censusRig is a worker consuming character-census and achievement-census
// jobs from the in-memory queue, fetching from a fake Lodestone and storing
// through the batching writer into a temporary real Postgres.
type censusRig struct {
	queue        *mockqueue.Fake
	repo         contract.CharacterRepository
	achievements contract.AchievementRepository
	gated        *gatedRepo
	lodestone    *mocklodestone.Fake
	run          func()
	stop         func()
}

func newCensusRig(t *testing.T) *censusRig {
	t.Helper()
	driver := postgrestest.NewDriver(t)
	real := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	gated := &gatedRepo{CharacterRepository: real}
	w := writer.New(gated, writer.Config{BatchSize: 1, FlushInterval: time.Hour}, nil)

	svc := census.NewService(gated, achievements, mockrepo.NewCensusRunFake())
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	ls := mocklodestone.NewFake()
	reg := handler.NewRegistry()
	reg.Register(handler.EventCharacterCensus, handler.NewCharacterCensus(ls, svc, nil).WithWriter(w))
	reg.Register(handler.EventAchievementCensus, handler.NewAchievementCensus(ls, svc, nil).WithWriter(w))

	q := mockqueue.NewFake()
	wk := worker.New(q, reg, nil)
	rig := &censusRig{queue: q, repo: real, achievements: achievements, gated: gated, lodestone: ls}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	rig.run = func() {
		go func() {
			defer close(done)
			_ = wk.RunEvents(ctx, []string{handler.EventCharacterCensus, handler.EventAchievementCensus}, 1)
		}()
	}
	rig.stop = func() { cancel(); <-done }
	t.Cleanup(func() {
		cancel()
		_ = w.Close(context.Background())
	})
	return rig
}

func (r *censusRig) publish(t *testing.T, event string, payload any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	if err := r.queue.Publish(context.Background(), contract.QueueJob{Type: event, Payload: b}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func (r *censusRig) seed(t *testing.T, id uint32) {
	t.Helper()
	now := time.Now().UTC()
	rec := contract.CharacterRecord{ID: id, Name: "Seeded", World: "Ultros", FirstSeenAt: now, LastCensusAt: &now}
	if err := r.repo.Upsert(context.Background(), rec, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func (r *censusRig) fetchAchievements(earned, latest time.Time) {
	r.lodestone.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{
			Milestones:        []contract.AchievementResult{{AchievementID: 590, Earned: true, EarnedAt: earned}},
			LatestAchievement: &contract.AchievementResult{AchievementID: 999, Earned: true, EarnedAt: latest},
		}, nil
	}
}

func TestCharacterCensus_AckedOnlyAfterCommitAndStoresCharacterWithJobs(t *testing.T) {
	rig := newCensusRig(t)
	rig.gated.gate = make(chan struct{})
	rig.lodestone.FetchCharacterFunc = func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur",
			ClassJobs: []contract.ClassJobRecord{{ClassJobID: 19, Name: "Paladin", Level: 100}},
		}, nil
	}
	rig.publish(t, handler.EventCharacterCensus, handler.CharacterCensusPayload{CharacterID: 501})
	rig.run()
	defer rig.stop()

	eventually(t, "the commit to start", func() bool { return rig.gated.calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("job acked (%d) while its batch was still committing", n)
	}

	close(rig.gated.gate)
	eventually(t, "the job to be acked", func() bool { return len(rig.queue.Acked()) == 1 })
	got, err := rig.repo.Get(context.Background(), 501)
	if err != nil || got == nil || got.Name != "Char" || got.Region == "" {
		t.Fatalf("stored character = %+v, %v", got, err)
	}
	jobs, _ := rig.repo.GetJobs(context.Background(), 501)
	if len(jobs) != 1 || jobs[0].ClassJobID != 19 || jobs[0].Level != 100 {
		t.Fatalf("stored jobs = %+v", jobs)
	}
	if got.LastCensusAt == nil {
		t.Fatal("last_census_at not set")
	}
}

func TestCharacterCensus_FailedCommitLeavesJobUnacked(t *testing.T) {
	rig := newCensusRig(t)
	rig.gated.failErr = errors.New("commit failed")
	rig.lodestone.FetchCharacterFunc = func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}
	rig.publish(t, handler.EventCharacterCensus, handler.CharacterCensusPayload{CharacterID: 502})
	rig.run()
	defer rig.stop()

	eventually(t, "the job to be returned for retry", func() bool { return len(rig.queue.Retried()) == 1 })
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("acked %d jobs whose batch failed to commit", n)
	}
	if stored(t, rig.repo, 502) {
		t.Fatal("character stored although the batch failed")
	}
}

func TestAchievementCensus_AckedOnlyAfterCommitAndStoresMilestonesWithLatest(t *testing.T) {
	rig := newCensusRig(t)
	rig.seed(t, 601)
	earned := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	latest := earned.Add(time.Hour)
	rig.fetchAchievements(earned, latest)
	rig.gated.gate = make(chan struct{})
	rig.publish(t, handler.EventAchievementCensus, handler.AchievementCensusPayload{CharacterID: 601})
	rig.run()
	defer rig.stop()

	eventually(t, "the commit to start", func() bool { return rig.gated.calls.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("job acked (%d) while its batch was still committing", n)
	}
	if ms, _ := rig.achievements.ListCharacterMilestones(context.Background(), 601); len(ms) != 0 {
		t.Fatalf("milestones visible before commit: %+v", ms)
	}

	close(rig.gated.gate)
	eventually(t, "the job to be acked", func() bool { return len(rig.queue.Acked()) == 1 })
	ms, err := rig.achievements.ListCharacterMilestones(context.Background(), 601)
	if err != nil || len(ms) != 1 || ms[0].AchievementID != 590 || !ms[0].AchievedAt.Equal(earned) {
		t.Fatalf("milestones = %+v, %v", ms, err)
	}
	got, _ := rig.repo.Get(context.Background(), 601)
	if got == nil || got.LatestAchievementID == nil || *got.LatestAchievementID != 999 ||
		got.LatestAchievementAt == nil || !got.LatestAchievementAt.Equal(latest) {
		t.Fatalf("latest achievement = %+v", got)
	}
}

func TestAchievementCensus_FailedCommitStoresNeitherMilestonesNorLatest(t *testing.T) {
	rig := newCensusRig(t)
	rig.seed(t, 602)
	rig.fetchAchievements(time.Now().Add(-time.Hour), time.Now())
	rig.gated.failErr = errors.New("commit failed")
	rig.publish(t, handler.EventAchievementCensus, handler.AchievementCensusPayload{CharacterID: 602})
	rig.run()
	defer rig.stop()

	eventually(t, "the job to be returned for retry", func() bool { return len(rig.queue.Retried()) == 1 })
	if n := len(rig.queue.Acked()); n != 0 {
		t.Fatalf("acked %d jobs whose batch failed to commit", n)
	}
	if ms, _ := rig.achievements.ListCharacterMilestones(context.Background(), 602); len(ms) != 0 {
		t.Fatalf("milestones stored: %+v", ms)
	}
	if got, _ := rig.repo.Get(context.Background(), 602); got == nil || got.LatestAchievementID != nil {
		t.Fatalf("latest achievement stored: %+v", got)
	}
}
