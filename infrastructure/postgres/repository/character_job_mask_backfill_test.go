package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// seedPreMaskCharacters stores characters 4001..4005 with jobs, then clears
// their masks the way rows stored before the masks existed look. Character
// 4002 keeps a (deliberately odd) mask that the backfill must not touch.
func seedPreMaskCharacters(t *testing.T, driver contract.DatabaseDriver) {
	t.Helper()
	repo := repository.NewCharacterRepository(driver)
	now := time.Now().UTC().Truncate(time.Millisecond)
	var writes []contract.CharacterWrite
	for id := uint32(4001); id <= 4005; id++ {
		writes = append(writes, contract.CharacterWrite{
			Record: contract.CharacterRecord{ID: id, Name: "Old", World: "Balmung", FirstSeenAt: now},
			Jobs: []contract.ClassJobRecord{
				{CharacterID: id, ClassJobID: 3, Name: "Marauder", Level: 100},
				{CharacterID: id, ClassJobID: 36, Name: "Blue Mage", Level: 80},
				{CharacterID: id, ClassJobID: 16, Name: "Miner", Level: 12},
			},
		})
	}
	if err := repo.StoreResult(context.Background(), contract.CensusResult{Characters: writes}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, driver, `UPDATE characters SET jobs_levelled_mask = NULL, jobs_at_cap_mask = NULL WHERE id <> 4002`)
	mustExec(t, driver, `UPDATE characters SET jobs_levelled_mask = 7, jobs_at_cap_mask = 7 WHERE id = 4002`)
}

func TestBackfillJobMasksFillsNullMasksInBatchesAndResumes(t *testing.T) {
	driver := newTestDriver(t)
	seedPreMaskCharacters(t, driver)
	ctx := context.Background()

	backfill := repository.NewCharacterRepository(driver).(contract.JobMaskBackfillRepository)
	first, err := backfill.BackfillJobMasksBatch(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Scanned != 2 || first.Filled != 1 || first.Cursor != 4002 || first.Done {
		t.Fatalf("first batch = %+v, want 2 scanned, 1 filled, cursor 4002, not done", first)
	}
	if _, _, ok := storedMasks(t, driver, 4003); ok {
		t.Fatal("character 4003 filled before its batch")
	}

	// The process stops here; a new one resumes from the stored cursor.
	resumed := repository.NewCharacterRepository(driver).(contract.JobMaskBackfillRepository)
	var batches []contract.JobMaskBackfillBatch
	for {
		b, err := resumed.BackfillJobMasksBatch(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, b)
		if b.Done || len(batches) > 5 {
			break
		}
	}
	if len(batches) != 2 || batches[0].Scanned != 2 || batches[0].Cursor != 4004 ||
		batches[1].Scanned != 1 || batches[1].Cursor != 4005 || !batches[1].Done {
		t.Fatalf("resumed batches = %+v, want [2 scanned to 4004] then [1 scanned to 4005, done]", batches)
	}

	wantLevelled, wantAtCap := bits(21, 36, 16), bits(21, 36)
	for _, id := range []uint32{4001, 4003, 4004, 4005} {
		levelled, atCap, ok := storedMasks(t, driver, id)
		if !ok || levelled != wantLevelled || atCap != wantAtCap {
			t.Errorf("character %d masks = %b, %b (set %v); want %b, %b", id, levelled, atCap, ok, wantLevelled, wantAtCap)
		}
	}
	if levelled, atCap, _ := storedMasks(t, driver, 4002); levelled != 7 || atCap != 7 {
		t.Errorf("already-set masks of 4002 changed to %b, %b", levelled, atCap)
	}

	// A finished backfill has nothing left to do.
	again, err := resumed.BackfillJobMasksBatch(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if again.Scanned != 0 || again.Filled != 0 || !again.Done || again.Cursor != 4005 {
		t.Fatalf("batch after completion = %+v, want nothing scanned, done, cursor 4005", again)
	}
}

func TestBackfillJobMasksOfCharacterWithoutJobsIsZero(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	if err := repository.NewCharacterRepository(driver).Upsert(ctx,
		contract.CharacterRecord{ID: 4101, Name: "None", World: "Balmung", FirstSeenAt: time.Now().UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	mustExec(t, driver, `UPDATE characters SET jobs_levelled_mask = NULL, jobs_at_cap_mask = NULL`)

	b, err := repository.NewCharacterRepository(driver).(contract.JobMaskBackfillRepository).BackfillJobMasksBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if b.Filled != 1 || !b.Done {
		t.Fatalf("batch = %+v, want 1 filled, done", b)
	}
	if levelled, atCap, ok := storedMasks(t, driver, 4101); !ok || levelled != 0 || atCap != 0 {
		t.Fatalf("masks = %b, %b (set %v); want 0, 0", levelled, atCap, ok)
	}
}
