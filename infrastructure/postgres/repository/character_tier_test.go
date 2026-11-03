package repository_test

import (
	"context"
	"database/sql"
	"fmt"
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

	for _, id := range []uint32{9001, 9002} {
		if tier, changed := censusTier(t, driver, id); tier != contract.CensusTierDormant || changed != nil {
			t.Errorf("new character %d tier=%d changed=%v, want the dormant tier and no change", id, tier, changed)
		}
	}
}

// A character first seen at or past the created cut was created within
// hours of its first sighting, so the sighting is its last change: it lands
// in the active tier at once, at any level. Below the cut a first sighting
// stays no change, and a stored character keeps its own last change.
func TestCharacterTier_FirstSightingPastTheCreatedCutIsAChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver, repository.WithCreatedExactFromID(9500))

	before := batchWrite(9499, "Old", paladin(90))
	at := batchWrite(9500, "AtCut")
	past := privateWrite(9501, "Past", paladin(1))
	storeCharacter(t, repo, before)
	storeCharacter(t, repo, at)
	storeCharacter(t, repo, past)

	if tier, changed := censusTier(t, driver, 9499); tier != contract.CensusTierDormant || changed != nil {
		t.Errorf("below the cut: tier=%d changed=%v, want the dormant tier and no change", tier, changed)
	}
	for _, w := range []contract.CharacterWrite{at, past} {
		tier, changed := censusTier(t, driver, w.Record.ID)
		if tier != contract.CensusTierActive || changed == nil || !changed.Equal(w.Record.FirstSeenAt) {
			t.Errorf("%d past the cut: tier=%d changed=%v, want the active tier changed at first sighting %v", w.Record.ID, tier, changed, w.Record.FirstSeenAt)
		}
	}

	// Seen again unchanged, the character keeps its first sighting as its
	// last change.
	again := batchWrite(9500, "AtCut")
	storeCharacter(t, repo, again)
	if _, changed := censusTier(t, driver, 9500); changed == nil || !changed.Equal(at.Record.FirstSeenAt) {
		t.Errorf("seen again unchanged: changed=%v, want the first sighting %v", changed, at.Record.FirstSeenAt)
	}

	// Without a cut every first sighting stays no change.
	plain := repository.NewCharacterRepository(driver)
	storeCharacter(t, plain, batchWrite(9600, "NoCut"))
	if tier, changed := censusTier(t, driver, 9600); tier != contract.CensusTierDormant || changed != nil {
		t.Errorf("without a cut: tier=%d changed=%v, want the dormant tier and no change", tier, changed)
	}
}

func TestCharacterTier_CharacterThatChangesMovesToTheActiveTier(t *testing.T) {
	for _, private := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			changed contract.CharacterWrite
		}{
			{"job levelled", batchWrite(9101, "Same", paladin(91))},
			{"job unlocked", batchWrite(9101, "Same", paladin(90), contract.ClassJobRecord{ClassJobID: 21, Name: "Warrior", Level: 1})},
			{"renamed", batchWrite(9101, "Renamed", paladin(90))},
			{"world transfer", func() contract.CharacterWrite {
				w := batchWrite(9101, "Same", paladin(90))
				w.Record.World = "Gilgamesh"
				return w
			}()},
			{"switched job", func() contract.CharacterWrite {
				w := batchWrite(9101, "Same", paladin(90))
				w.Record.ActiveJob = "Warrior"
				return w
			}()},
			{"joined a Free Company", func() contract.CharacterWrite {
				w := batchWrite(9101, "Same", paladin(90))
				fc := "9229001536389012345"
				w.Record.FreeCompanyID = &fc
				return w
			}()},
		} {
			t.Run(fmt.Sprintf("%s private=%v", tc.name, private), func(t *testing.T) {
				driver := newTestDriver(t)
				repo := repository.NewCharacterRepository(driver)
				before := batchWrite(9101, "Same", paladin(90))
				before.Record.ActiveJob = "Paladin"
				before.Record.AchievementsPrivate = private
				storeCharacter(t, repo, before)

				tc.changed.Record.AchievementsPrivate = private
				storeCharacter(t, repo, tc.changed)

				tier, changed := censusTier(t, driver, 9101)
				if tier != contract.CensusTierActive || changed == nil || !changed.Equal(*tc.changed.Record.LastCensusAt) {
					t.Fatalf("tier=%d changed=%v, want the active tier changed at the census %v", tier, changed, *tc.changed.Record.LastCensusAt)
				}
			})
		}
	}
}

func TestCharacterTier_UnchangedCharacterFallsBackByItsLastActivity(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	for id, set := range map[uint32]string{
		9201: `last_change_at = NOW() - INTERVAL '10 days'`,
		9202: `last_change_at = NOW() - INTERVAL '100 days'`,
		9203: `last_change_at = NOW() - INTERVAL '400 days'`,
		9204: `latest_achievement_at = NOW() - INTERVAL '10 days'`,
		9205: `latest_achievement_at = NOW() - INTERVAL '200 days'`,
		// Seeded into tier 2 by migration 00031 without a recorded change.
		9206: `census_tier = 2`,
	} {
		storeCharacter(t, repo, privateWrite(id, "Same", paladin(90)))
		mustExec(t, driver, `UPDATE characters SET `+set+` WHERE id = $1`, id)
	}

	for id := uint32(9201); id <= 9206; id++ {
		storeCharacter(t, repo, privateWrite(id, "Same", paladin(90)))
	}

	for id, want := range map[uint32]int{
		9201: contract.CensusTierActive, 9202: contract.CensusTierRecent, 9203: contract.CensusTierDormant,
		9204: contract.CensusTierActive, 9205: contract.CensusTierRecent, 9206: contract.CensusTierDormant,
	} {
		if tier, _ := censusTier(t, driver, id); tier != want {
			t.Errorf("character %d tier = %d, want %d", id, tier, want)
		}
	}
}

func TestCharacterTier_WindowsAreConfigurable(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver, repository.WithActiveWindowDays(7), repository.WithRecentWindowDays(30))
	storeCharacter(t, repo, privateWrite(9301, "Week", paladin(90)))
	storeCharacter(t, repo, privateWrite(9302, "Month", paladin(90)))
	mustExec(t, driver, `UPDATE characters SET last_change_at = NOW() - INTERVAL '10 days' WHERE id = 9301`)
	mustExec(t, driver, `UPDATE characters SET last_change_at = NOW() - INTERVAL '40 days' WHERE id = 9302`)

	storeCharacter(t, repo, privateWrite(9301, "Week", paladin(90)))
	storeCharacter(t, repo, privateWrite(9302, "Month", paladin(90)))

	if tier, _ := censusTier(t, driver, 9301); tier != contract.CensusTierRecent {
		t.Errorf("tier = %d, want recent once the change is older than a 7-day window", tier)
	}
	if tier, _ := censusTier(t, driver, 9302); tier != contract.CensusTierDormant {
		t.Errorf("tier = %d, want dormant once the change is older than a 30-day recent window", tier)
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

	if tier, changed := censusTier(t, driver, 9361); tier != contract.CensusTierDormant || changed != nil {
		t.Errorf("tier=%d changed=%v, want dormant and no change when the job was not known before", tier, changed)
	}
}

func TestCharacterTier_EmptyProfileIsNotAChange(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	storeCharacter(t, repo, privateWrite(9351, "Kept", paladin(90)))

	// A profile read with no jobs and no name must not count as a change.
	storeCharacter(t, repo, privateWrite(9351, ""))

	if tier, changed := censusTier(t, driver, 9351); tier != contract.CensusTierDormant || changed != nil {
		t.Errorf("tier=%d changed=%v, want dormant and no change", tier, changed)
	}
}

func TestCharacterTier_AchievementWrites(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	now := time.Now().UTC()
	old, recent, fresh := now.AddDate(-2, 0, 0), now.AddDate(0, 0, -200), now.AddDate(0, 0, -10)
	id5, id6 := uint32(5), uint32(6)
	achievement := func(id uint32, latestID *uint32, at time.Time) {
		store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: id, LatestID: latestID, LatestAt: &at}}})
	}

	// 9401: a long-dormant achiever earns a newer achievement.
	storeCharacter(t, repo, batchWrite(9401, "Achiever", paladin(90)))
	achievement(9401, &id5, old)
	if tier, changed := censusTier(t, driver, 9401); tier != contract.CensusTierDormant || changed != nil {
		t.Fatalf("first achievement read tier=%d changed=%v, want dormant and no change", tier, changed)
	}
	achievement(9401, &id6, old.Add(48*time.Hour))
	if tier, changed := censusTier(t, driver, 9401); tier != contract.CensusTierActive || changed == nil {
		t.Errorf("newer achievement tier=%d changed=%v, want active with a change", tier, changed)
	}

	// 9402, 9403: the achievement date alone places a first read.
	storeCharacter(t, repo, batchWrite(9402, "Recent", paladin(90)))
	achievement(9402, &id5, recent)
	storeCharacter(t, repo, batchWrite(9403, "Fresh", paladin(90)))
	achievement(9403, &id5, fresh)
	if tier, _ := censusTier(t, driver, 9402); tier != contract.CensusTierRecent {
		t.Errorf("achievement 200 days ago tier = %d, want recent", tier)
	}
	if tier, _ := censusTier(t, driver, 9403); tier != contract.CensusTierActive {
		t.Errorf("achievement 10 days ago tier = %d, want active", tier)
	}
	// A later unchanged profile keeps the stored achievement's tier.
	storeCharacter(t, repo, batchWrite(9403, "Fresh", paladin(90)))
	if tier, _ := censusTier(t, driver, 9403); tier != contract.CensusTierActive {
		t.Errorf("unchanged profile after a fresh achievement tier = %d, want active", tier)
	}

	// 9404: found private, with no activity.
	storeCharacter(t, repo, batchWrite(9404, "Hides", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9404, Private: true}}})
	if tier, _ := censusTier(t, driver, 9404); tier != contract.CensusTierDormant {
		t.Errorf("found private tier = %d, want dormant", tier)
	}

	// 9405: found private right after a change.
	storeCharacter(t, repo, batchWrite(9405, "Before", paladin(90)))
	storeCharacter(t, repo, batchWrite(9405, "After", paladin(90)))
	store(t, repo, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: 9405, Private: true}}})
	if tier, _ := censusTier(t, driver, 9405); tier != contract.CensusTierActive {
		t.Errorf("found private after a change tier = %d, want active", tier)
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
