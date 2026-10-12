package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestCharacterRepository_StoreResultStoresCharactersAndAchievementsTogether(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	ctx := context.Background()

	if err := repo.Upsert(ctx, batchWrite(8001, "Seeded").Record, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	earned := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	latestID := uint32(777)
	latestAt := earned.Add(time.Hour)

	err := repo.StoreResult(ctx, contract.CensusResult{
		Characters: []contract.CharacterWrite{batchWrite(8002, "New", contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: 100})},
		Achievements: []contract.AchievementWrite{{
			CharacterID: 8001,
			Milestones:  []contract.CharacterMilestone{{CharacterID: 8001, AchievementID: 590, AchievedAt: earned}},
			LatestID:    &latestID,
			LatestAt:    &latestAt,
		}},
	})
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	if got, _ := repo.Get(ctx, 8002); got == nil || got.Name != "New" {
		t.Fatalf("Get(8002) = %+v", got)
	}
	if jobs := jobsByID(t, repo, 8002); len(jobs) != 1 || jobs[19].Level != 100 {
		t.Fatalf("jobs of 8002 = %+v", jobs)
	}
	seeded, _ := repo.Get(ctx, 8001)
	if seeded == nil || seeded.LatestAchievementID == nil || *seeded.LatestAchievementID != 777 ||
		seeded.LatestAchievementAt == nil || !seeded.LatestAchievementAt.Equal(latestAt) || seeded.AchievementsPrivate {
		t.Fatalf("8001 summary = %+v", seeded)
	}
	ms, err := achievements.ListCharacterMilestones(ctx, 8001)
	if err != nil || len(ms) != 1 || ms[0].AchievementID != 590 || !ms[0].AchievedAt.Equal(earned) {
		t.Fatalf("milestones = %+v, %v", ms, err)
	}
}

func TestCharacterRepository_StoreResultPrivateAchievementsKeepPriorData(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	ctx := context.Background()

	if err := repo.Upsert(ctx, batchWrite(8101, "Priv").Record, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	earned := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	latestID := uint32(5)
	if err := repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{
		CharacterID: 8101, LatestID: &latestID, LatestAt: &earned,
		Milestones: []contract.CharacterMilestone{{CharacterID: 8101, AchievementID: 590, AchievedAt: earned}},
	}}}); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	if err := repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 8101, Private: true}}}); err != nil {
		t.Fatalf("StoreResult private: %v", err)
	}

	got, _ := repo.Get(ctx, 8101)
	if got == nil || !got.AchievementsPrivate || got.LatestAchievementID == nil || *got.LatestAchievementID != 5 {
		t.Fatalf("after private write = %+v, want private with latest kept", got)
	}
	if ms, _ := achievements.ListCharacterMilestones(ctx, 8101); len(ms) != 1 {
		t.Fatalf("milestones = %+v, want the earlier one kept", ms)
	}
}

func TestCharacterRepository_StoreResultIsAtomicAcrossCharactersAndAchievements(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	achievements := repository.NewAchievementRepository(driver)
	ctx := context.Background()

	if err := repo.Upsert(ctx, batchWrite(8201, "Seeded").Record, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	earned := time.Now().UTC()
	latestID := uint32(9)

	// 4e9 overflows the INTEGER achievement_id column, failing the milestones
	// insert after the character and summary statements already ran.
	err := repo.StoreResult(ctx, contract.CensusResult{
		Characters: []contract.CharacterWrite{batchWrite(8202, "Fine")},
		Achievements: []contract.AchievementWrite{{
			CharacterID: 8201,
			Milestones:  []contract.CharacterMilestone{{CharacterID: 8201, AchievementID: 4_000_000_000, AchievedAt: earned}},
			LatestID:    &latestID, LatestAt: &earned,
		}},
	})
	if err == nil {
		t.Fatal("StoreResult succeeded, want error")
	}
	if got, _ := repo.Get(ctx, 8202); got != nil {
		t.Fatalf("character 8202 committed despite a failed result: %+v", got)
	}
	if got, _ := repo.Get(ctx, 8201); got == nil || got.LatestAchievementID != nil {
		t.Fatalf("latest achievement of 8201 committed despite a failed result: %+v", got)
	}
	if ms, _ := achievements.ListCharacterMilestones(ctx, 8201); len(ms) != 0 {
		t.Fatalf("milestones committed: %+v", ms)
	}
}
