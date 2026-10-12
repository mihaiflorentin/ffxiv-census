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

func TestCharacterRepository_StoreResultRecordsWhenThePrivateFlagWasVerified(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	if err := repo.Upsert(ctx, batchWrite(8151, "Priv").Record, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got, _ := repo.Get(ctx, 8151); got == nil || got.AchievementsCheckedAt != nil {
		t.Fatalf("a never-checked character has achievements_checked_at = %+v, want nil", got)
	}

	checkedAt := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	if err := repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 8151, Private: true, CheckedAt: checkedAt}}}); err != nil {
		t.Fatalf("StoreResult private: %v", err)
	}
	got, _ := repo.Get(ctx, 8151)
	if got == nil || !got.AchievementsPrivate || got.AchievementsCheckedAt == nil || !got.AchievementsCheckedAt.Equal(checkedAt) {
		t.Fatalf("after private write = %+v, want private checked at %v", got, checkedAt)
	}

	// A character refresh leaves the verification time alone.
	if err := repo.Upsert(ctx, batchWrite(8151, "Priv").Record, nil); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	// So does a private write without a time.
	if err := repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 8151, Private: true}}}); err != nil {
		t.Fatalf("StoreResult private without time: %v", err)
	}
	got, _ = repo.Get(ctx, 8151)
	if got == nil || !got.AchievementsPrivate || got.AchievementsCheckedAt == nil || !got.AchievementsCheckedAt.Equal(checkedAt) {
		t.Fatalf("after refresh = %+v, want the check time kept at %v", got, checkedAt)
	}

	later := checkedAt.Add(31 * 24 * time.Hour)
	if err := repo.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 8151, Private: true, CheckedAt: later}}}); err != nil {
		t.Fatalf("StoreResult second check: %v", err)
	}
	if got, _ = repo.Get(ctx, 8151); got == nil || got.AchievementsCheckedAt == nil || !got.AchievementsCheckedAt.Equal(later) {
		t.Fatalf("after second check = %+v, want %v", got, later)
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
