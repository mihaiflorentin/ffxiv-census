package repository_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// logStatements records one row per statement-level trigger firing on the
// characters and character_jobs tables, so tests can count how many write
// statements a StoreResult issued.
func logStatements(t *testing.T, driver contract.DatabaseDriver) {
	t.Helper()
	mustExec(t, driver, `CREATE TABLE statement_log (tbl TEXT NOT NULL, op TEXT NOT NULL)`)
	mustExec(t, driver, `CREATE FUNCTION log_statement() RETURNS trigger AS $$
		BEGIN INSERT INTO statement_log VALUES (TG_TABLE_NAME, TG_OP); RETURN NULL; END
		$$ LANGUAGE plpgsql`)
	for _, tbl := range []string{"characters", "character_jobs"} {
		for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
			mustExec(t, driver, `CREATE TRIGGER `+tbl+`_`+op+`_stmt_log AFTER `+op+` ON `+tbl+`
				FOR EACH STATEMENT EXECUTE FUNCTION log_statement()`)
		}
	}
}

func statementCount(t *testing.T, driver contract.DatabaseDriver, tbl, op string) int {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM statement_log WHERE tbl = $1 AND op = $2`, tbl, op).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCharacterRepository_StoreResultSharesStatementsAcrossCharacters(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	logStatements(t, driver)

	err := storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(9301, "One", contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: 100}),
		batchWrite(9302, "Two", contract.ClassJobRecord{ClassJobID: 16, Name: "Miner", Level: 90}),
		batchWrite(9303, "Three"),
		batchWrite(9304, "Four", contract.ClassJobRecord{ClassJobID: 1, Name: "Gladiator", Level: 5}),
	})
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}

	for _, c := range []struct {
		tbl, op string
		want    int
	}{
		{"characters", "INSERT", 1},
		{"character_jobs", "INSERT", 1},
		{"character_jobs", "DELETE", 1},
	} {
		if got := statementCount(t, driver, c.tbl, c.op); got != c.want {
			t.Errorf("%s %s statements = %d, want %d", c.tbl, c.op, got, c.want)
		}
	}
}

func TestCharacterRepository_StoreResultStoresSameRowsAsOneByOne(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	fcID, fcName := "fc1", "Free"
	latestID := uint32(55)
	writes := []contract.CharacterWrite{
		{Record: contract.CharacterRecord{
			ID: 9402, Name: "Full", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Elezen", Tribe: "Wildwood",
			Gender: 1, GrandCompany: "Maelstrom", FreeCompanyID: &fcID, FreeCompanyName: &fcName, Bio: "hi", ActiveJob: "PLD",
			ItemLevel: 700, AchievementsPrivate: true, LatestAchievementID: &latestID, LatestAchievementAt: &now,
			FirstSeenAt: now, LastCensusAt: &now,
		}, Jobs: []contract.ClassJobRecord{{CharacterID: 9402, ClassJobID: 19, Name: "Paladin", Level: 100, ExpLevel: 3}, {CharacterID: 9402, ClassJobID: 1, Name: "Gladiator", Level: 60}}},
		batchWrite(9401, "Bare", contract.ClassJobRecord{ClassJobID: 30, Name: "Ninja", Level: 80}),
		batchWrite(9403, "Revived"),
	}

	snapshot := func(batched bool) map[uint32]any {
		driver := newTestDriver(t)
		repo := repository.NewCharacterRepository(driver)
		ctx := context.Background()
		// Existing rows exercise the ON CONFLICT branch: keep the latest
		// achievement and the private flag, drop a stale job, clear deleted_at.
		prevAt := now.Add(-48 * time.Hour)
		prevID := uint32(7)
		seed := batchWrite(9401, "Old", contract.ClassJobRecord{ClassJobID: 2, Name: "Pugilist", Level: 1})
		seed.Record.LatestAchievementID, seed.Record.LatestAchievementAt, seed.Record.AchievementsPrivate = &prevID, &prevAt, true
		gone := batchWrite(9403, "Gone")
		gone.Record.DeletedAt = &prevAt
		seed.Record.FirstSeenAt, gone.Record.FirstSeenAt = now, now // first_seen_at survives the upsert
		if err := storeCharacters(ctx, repo, []contract.CharacterWrite{seed, gone}); err != nil {
			t.Fatal(err)
		}
		if batched {
			if err := storeCharacters(ctx, repo, writes); err != nil {
				t.Fatalf("StoreResult: %v", err)
			}
		} else {
			for _, w := range writes {
				if err := storeCharacters(ctx, repo, []contract.CharacterWrite{w}); err != nil {
					t.Fatalf("StoreResult one: %v", err)
				}
			}
		}
		out := map[uint32]any{}
		for _, w := range writes {
			got, err := repo.Get(ctx, w.Record.ID)
			if err != nil || got == nil {
				t.Fatalf("Get(%d) = %+v, %v", w.Record.ID, got, err)
			}
			out[w.Record.ID] = []any{*got, jobsByID(t, repo, w.Record.ID)}
		}
		return out
	}

	one, batch := snapshot(false), snapshot(true)
	if !reflect.DeepEqual(one, batch) {
		t.Fatalf("batched rows differ from one-by-one rows:\n one   = %+v\n batch = %+v", one, batch)
	}
}

func TestCharacterRepository_StoreResultKeepsLastDuplicateCharacter(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()

	err := storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(9501, "First", contract.ClassJobRecord{ClassJobID: 1, Name: "Gladiator", Level: 10}),
		batchWrite(9502, "Other"),
		batchWrite(9501, "Last", contract.ClassJobRecord{ClassJobID: 19, Name: "Paladin", Level: 100}),
	})
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	if got, _ := repo.Get(ctx, 9501); got == nil || got.Name != "Last" {
		t.Fatalf("Get(9501) = %+v, want the last record", got)
	}
	if jobs := jobsByID(t, repo, 9501); len(jobs) != 1 || jobs[19].Level != 100 {
		t.Fatalf("jobs of 9501 = %+v, want only the last record's job", jobs)
	}
}

func TestCharacterRepository_StoreResultRemovesJobsPerCharacter(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	job := func(id uint8, name string) contract.ClassJobRecord {
		return contract.ClassJobRecord{ClassJobID: id, Name: name, Level: 50}
	}
	err := storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(9601, "A", job(1, "Gladiator"), job(16, "Miner")),
		batchWrite(9602, "B", job(1, "Gladiator"), job(16, "Miner")),
		batchWrite(9603, "C", job(1, "Gladiator")),
	})
	if err != nil {
		t.Fatal(err)
	}

	// 9601 drops Miner, 9602 keeps both, 9603 drops everything.
	err = storeCharacters(ctx, repo, []contract.CharacterWrite{
		batchWrite(9601, "A", job(1, "Gladiator")),
		batchWrite(9602, "B", job(1, "Gladiator"), job(16, "Miner")),
		batchWrite(9603, "C"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uint32][]uint8{9601: {1}, 9602: {1, 16}, 9603: nil} {
		got := jobsByID(t, repo, id)
		if len(got) != len(want) {
			t.Fatalf("jobs of %d = %+v, want ids %v", id, got, want)
		}
		for _, w := range want {
			if _, ok := got[w]; !ok {
				t.Fatalf("jobs of %d = %+v, want ids %v", id, got, want)
			}
		}
	}
}

func TestCharacterRepository_StoreResultMixedBatch(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver)
	ctx := context.Background()
	earned := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	id1, id2 := uint32(1), uint32(2)

	// 9701 is both written as a character (with a latest achievement of its
	// own) and updated by an achievement result in the same batch; the
	// achievement is applied after the character.
	w := batchWrite(9701, "Mixed")
	w.Record.LatestAchievementID = &id1
	err := repo.StoreResult(ctx, contract.CensusResult{
		Characters: []contract.CharacterWrite{w, batchWrite(9702, "Plain")},
		Achievements: []contract.AchievementWrite{
			{CharacterID: 9701, LatestID: &id2, LatestAt: &earned, Milestones: []contract.CharacterMilestone{{CharacterID: 9701, AchievementID: 590, AchievedAt: earned}}},
			{CharacterID: 9702, Private: true},
		},
	})
	if err != nil {
		t.Fatalf("StoreResult: %v", err)
	}
	if got, _ := repo.Get(ctx, 9701); got == nil || got.LatestAchievementID == nil || *got.LatestAchievementID != 2 {
		t.Fatalf("9701 = %+v, want achievement summary applied after the character", got)
	}
	if got, _ := repo.Get(ctx, 9702); got == nil || !got.AchievementsPrivate {
		t.Fatalf("9702 = %+v, want private", got)
	}
}
