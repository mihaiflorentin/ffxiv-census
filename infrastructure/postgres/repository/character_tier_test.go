package repository_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// censusTier reads a character's census tier and last change time.
func censusTier(t *testing.T, driver contract.DatabaseDriver, id uint32) (int, *time.Time) {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tier int
	var changed sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT census_tier, last_change_at FROM characters WHERE id = $1`, id).Scan(&tier, &changed); err != nil {
		t.Fatalf("read tier of %d: %v", id, err)
	}
	if !changed.Valid {
		return tier, nil
	}
	return tier, &changed.Time
}

// privateWrite is batchWrite for a character that hides its achievements.
func privateWrite(id uint32, name string, jobs ...contract.ClassJobRecord) contract.CharacterWrite {
	w := batchWrite(id, name, jobs...)
	w.Record.AchievementsPrivate = true
	return w
}

func paladin(level uint8) contract.ClassJobRecord {
	return contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: level}
}

func store(t *testing.T, repo contract.CharacterRepository, result contract.CensusResult) {
	t.Helper()
	if err := repo.StoreResult(context.Background(), result); err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
}

func storeCharacter(t *testing.T, repo contract.CharacterRepository, w contract.CharacterWrite) {
	t.Helper()
	store(t, repo, contract.CensusResult{Characters: []contract.CharacterWrite{w}})
}

func TestCharacterTier_FirstSightingIsNotAChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)

	storeCharacter(t, repo, batchWrite(9001, "Public", paladin(90)))
	storeCharacter(t, repo, privateWrite(9002, "Private", paladin(90)))

	if tier, changed := censusTier(t, driver, 9001); tier != 1 || changed != nil {
		t.Errorf("new public character tier=%d changed=%v, want tier 1, no change", tier, changed)
	}
	if tier, changed := censusTier(t, driver, 9002); tier != 3 || changed != nil {
		t.Errorf("new private character tier=%d changed=%v, want tier 3, no change", tier, changed)
	}
}

func TestCharacterTier_PrivateCharacterThatChangesMovesToTheActiveTier(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changed contract.CharacterWrite
	}{
		{"job levelled", privateWrite(9101, "Same", paladin(91))},
		{"job unlocked", privateWrite(9101, "Same", paladin(90), contract.ClassJobRecord{ClassJobID: 21, Name: "Warrior", Level: 1})},
		{"renamed", privateWrite(9101, "Renamed", paladin(90))},
		{"world transfer", func() contract.CharacterWrite {
			w := privateWrite(9101, "Same", paladin(90))
			w.Record.World = "Gilgamesh"
			return w
		}()},
		{"switched job", func() contract.CharacterWrite {
			w := privateWrite(9101, "Same", paladin(90))
			w.Record.ActiveJob = "Warrior"
			return w
		}()},
		{"joined a Free Company", func() contract.CharacterWrite {
			w := privateWrite(9101, "Same", paladin(90))
			fc := "9229001536389012345"
			w.Record.FreeCompanyID = &fc
			return w
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			driver := newTestDriver(t)
			repo := repository.NewCharacterRepository(driver)
			before := privateWrite(9101, "Same", paladin(90))
			before.Record.ActiveJob = "Paladin"
			storeCharacter(t, repo, before)

			storeCharacter(t, repo, tc.changed)

			tier, changed := censusTier(t, driver, 9101)
			if tier != 2 || changed == nil || !changed.Equal(*tc.changed.Record.LastCensusAt) {
				t.Fatalf("tier=%d changed=%v, want tier 2 changed at the census %v", tier, changed, *tc.changed.Record.LastCensusAt)
			}
		})
	}
}

func TestCharacterTier_UnchangedPrivateCharacterStaysOrFallsBack(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	storeCharacter(t, repo, privateWrite(9201, "Recent", paladin(90)))
	storeCharacter(t, repo, privateWrite(9202, "Stale", paladin(90)))
	storeCharacter(t, repo, privateWrite(9203, "Never", paladin(90)))
	mustExec(t, driver, `UPDATE characters SET census_tier = 2, last_change_at = NOW() - INTERVAL '10 days' WHERE id = 9201`)
	mustExec(t, driver, `UPDATE characters SET census_tier = 2, last_change_at = NOW() - INTERVAL '100 days' WHERE id = 9202`)
	// Seeded by the migration: active tier without a recorded change.
	mustExec(t, driver, `UPDATE characters SET census_tier = 2 WHERE id = 9203`)

	for _, id := range []uint32{9201, 9202, 9203} {
		storeCharacter(t, repo, privateWrite(id, map[uint32]string{9201: "Recent", 9202: "Stale", 9203: "Never"}[id], paladin(90)))
	}

	for id, want := range map[uint32]int{9201: 2, 9202: 3, 9203: 3} {
		if tier, _ := censusTier(t, driver, id); tier != want {
			t.Errorf("character %d tier = %d, want %d", id, tier, want)
		}
	}
}

func TestCharacterTier_ActiveWindowIsConfigurable(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver, repository.WithActiveWindowDays(7))
	storeCharacter(t, repo, privateWrite(9301, "Week", paladin(90)))
	mustExec(t, driver, `UPDATE characters SET census_tier = 2, last_change_at = NOW() - INTERVAL '10 days' WHERE id = 9301`)

	storeCharacter(t, repo, privateWrite(9301, "Week", paladin(90)))

	if tier, _ := censusTier(t, driver, 9301); tier != 3 {
		t.Errorf("tier = %d, want 3 once the change is older than a 7-day window", tier)
	}
}

func TestCharacterTier_FirstKnownActiveJobIsNotAChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	// Stored before the active job was parsed: empty.
	storeCharacter(t, repo, privateWrite(9361, "Same", paladin(90)))

	w := privateWrite(9361, "Same", paladin(90))
	w.Record.ActiveJob = "Paladin"
	storeCharacter(t, repo, w)

	if tier, changed := censusTier(t, driver, 9361); tier != 3 || changed != nil {
		t.Errorf("tier=%d changed=%v, want tier 3 and no change when the job was not known before", tier, changed)
	}
}

func TestCharacterTier_EmptyProfileIsNotAChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	storeCharacter(t, repo, privateWrite(9351, "Kept", paladin(90)))

	// A profile read with no jobs and no name must not count as a change.
	storeCharacter(t, repo, privateWrite(9351, ""))

	if tier, changed := censusTier(t, driver, 9351); tier != 3 || changed != nil {
		t.Errorf("tier=%d changed=%v, want tier 3 and no change", tier, changed)
	}
}

func TestCharacterTier_AchievementWrites(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(48 * time.Hour)
	id5, id6 := uint32(5), uint32(6)

	// 9401: public with a known latest achievement that gets a newer one.
	storeCharacter(t, repo, batchWrite(9401, "Achiever", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9401, LatestID: &id5, LatestAt: &old}}})
	if tier, changed := censusTier(t, driver, 9401); tier != 1 || changed != nil {
		t.Fatalf("first achievement read tier=%d changed=%v, want tier 1 and no change", tier, changed)
	}
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9401, LatestID: &id6, LatestAt: &newer}}})
	if tier, changed := censusTier(t, driver, 9401); tier != 1 || changed == nil {
		t.Errorf("newer achievement tier=%d changed=%v, want tier 1 with a change", tier, changed)
	}

	// 9402: public character found private, with no recent change.
	storeCharacter(t, repo, batchWrite(9402, "Hides", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9402, Private: true}}})
	if tier, _ := censusTier(t, driver, 9402); tier != 3 {
		t.Errorf("found private tier = %d, want 3", tier)
	}

	// 9403: public character found private right after a change.
	storeCharacter(t, repo, batchWrite(9403, "Before", paladin(90)))
	storeCharacter(t, repo, batchWrite(9403, "After", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9403, Private: true}}})
	if tier, _ := censusTier(t, driver, 9403); tier != 2 {
		t.Errorf("found private after a change tier = %d, want 2", tier)
	}

	// 9404: private character whose achievements turn public.
	storeCharacter(t, repo, privateWrite(9404, "Opens", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9404, LatestID: &id5, LatestAt: &old}}})
	if tier, _ := censusTier(t, driver, 9404); tier != 1 {
		t.Errorf("turned public tier = %d, want 1", tier)
	}
}

func TestCharacterRepository_ListStaleIDsInTier(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	for _, id := range []uint32{9501, 9502, 9503, 9504, 9505} {
		storeCharacter(t, repo, privateWrite(id, "C", paladin(90)))
	}
	mustExec(t, driver, `UPDATE characters SET census_tier = 2 WHERE id IN (9501, 9502, 9503)`)
	mustExec(t, driver, `UPDATE characters SET last_census_at = NOW() - INTERVAL '1 day' WHERE id = 9501`)
	mustExec(t, driver, `UPDATE characters SET last_census_at = NOW() - INTERVAL '3 days' WHERE id = 9502`)
	mustExec(t, driver, `UPDATE characters SET last_census_at = NOW() - INTERVAL '2 days', deleted_at = NOW() WHERE id = 9503`)

	got, err := repo.ListStaleIDsInTier(ctx, 2, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint32{9502, 9501}; !slices.Equal(got, want) {
		t.Errorf("tier 2 = %v, want %v (oldest first, no deleted, no other tier)", got, want)
	}
	got, err = repo.ListStaleIDsInTier(ctx, 2, time.Now().Add(-2*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uint32{9502}; !slices.Equal(got, want) {
		t.Errorf("tier 2 older than 2 days = %v, want %v", got, want)
	}
	if got, _ := repo.ListStaleIDsInTier(ctx, 3, time.Time{}, 1); len(got) != 1 {
		t.Errorf("tier 3 with limit 1 = %v, want one id", got)
	}
}
