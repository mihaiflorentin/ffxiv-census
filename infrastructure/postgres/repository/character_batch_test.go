package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// setLatestAchievement sets a character's latest-achievement time (and clears
// its private flag) through the one write path, StoreResult.
func setLatestAchievement(ctx context.Context, repo contract.CharacterRepository, id uint32, at *time.Time) error {
	return repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: id, LatestAt: at}}})
}

// storeCharacters stores character writes as one StoreResult transaction.
func storeCharacters(ctx context.Context, repo contract.CharacterRepository, writes []contract.CharacterWrite) error {
	return repo.StoreResult(ctx, contract.CensusResult{Characters: writes})
}

func batchWrite(id uint32, name string, jobs ...contract.ClassJobRecord) contract.CharacterWrite {
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := range jobs {
		jobs[i].CharacterID = id
	}
	return contract.CharacterWrite{
		Record: contract.CharacterRecord{ID: id, Name: name, World: "Balmung", FirstSeenAt: now, LastCensusAt: &now},
		Jobs:   jobs,
	}
}

func TestCharacterRepository_StoreResultStoresEveryCharacterAndItsJobs(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	// Character 7002 already exists with a job that its new profile no longer has.
	if err := repo.Upsert(ctx, batchWrite(7002, "Old").Record, []contract.ClassJobRecord{{CharacterID: 7002, ClassJobID: 1, Name: "Gladiator", Level: 10}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(7001, "One", contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: 100}),
		batchWrite(7002, "Two", contract.ClassJobRecord{ClassJobID: 16, Name: "Miner", Level: 90}),
		batchWrite(7003, "Three"),
	})
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	for id, name := range map[uint32]string{7001: "One", 7002: "Two", 7003: "Three"} {
		got, err := repo.Get(ctx, id)
		if err != nil || got == nil || got.Name != name {
			t.Fatalf("Get(%d) = %+v, %v; want name %q", id, got, err, name)
		}
	}
	if jobs := jobsByID(t, repo, 7001); len(jobs) != 1 || jobs[19].Level != 100 {
		t.Fatalf("jobs of 7001 = %+v", jobs)
	}
	if jobs := jobsByID(t, repo, 7002); len(jobs) != 1 || jobs[16].Level != 90 {
		t.Fatalf("jobs of 7002 = %+v, want only Miner (Gladiator removed)", jobs)
	}
}

func TestCharacterRepository_StoreResultIsAtomic(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	// A NUL byte is rejected by Postgres text columns, failing the second write.
	err := storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(7101, "Fine"),
		batchWrite(7102, "Bad\x00Name"),
	})
	if err == nil {
		t.Fatal("StoreResult succeeded, want error")
	}
	if got, _ := repo.Get(ctx, 7101); got != nil {
		t.Fatalf("character 7101 committed despite a failed batch: %+v", got)
	}
}

func TestCharacterRepository_StoreResultEmptyIsNoop(t *testing.T) {
	repo := repository.NewCharacterRepository(newTestDriver(t))
	if err := storeCharacters(context.Background(), repo, nil); err != nil {
		t.Fatalf("StoreResult(nil): %v", err)
	}
}

func TestCharacterRepository_StoreResultMarksRejectedDataAsInvalid(t *testing.T) {
	repo := repository.NewCharacterRepository(newTestDriver(t))
	err := storeCharacters(context.Background(), repo, []contract.CharacterWrite{batchWrite(7201, "Bad\x00Name")})
	if !errors.Is(err, contract.ErrInvalidData) {
		t.Fatalf("StoreResult error = %v, want ErrInvalidData for a value the database rejects", err)
	}
}

func TestCharacterRepository_StoreResultDoesNotMarkInfrastructureErrorsAsInvalid(t *testing.T) {
	repo := repository.NewCharacterRepository(newTestDriver(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := storeCharacters(ctx, repo, []contract.CharacterWrite{batchWrite(7202, "Fine")})
	if err == nil || errors.Is(err, contract.ErrInvalidData) {
		t.Fatalf("StoreResult error = %v, want a non-ErrInvalidData failure", err)
	}
}
