package repository_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestJobMaskColumnsAreNullableBigintsWithNoDefault(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()

	for _, col := range []string{"jobs_levelled_mask", "jobs_at_cap_mask"} {
		row, err := driver.FetchOne(ctx,
			`SELECT is_nullable, data_type, column_default
			   FROM information_schema.columns
			  WHERE table_name = 'characters' AND column_name = $1`, col)
		if err != nil {
			t.Fatal(err)
		}
		var nullable, dataType string
		var def sql.NullString
		if err := row.Scan(&nullable, &dataType, &def); err != nil {
			t.Fatalf("characters.%s missing: %v", col, err)
		}
		if nullable != "YES" || dataType != "bigint" || def.Valid {
			t.Errorf("characters.%s nullable=%q type=%q default=%v, want nullable bigint with no default", col, nullable, dataType, def)
		}
	}
}

// storedMasks reads a character's two job bitmasks; ok is false while they are NULL.
func storedMasks(t *testing.T, driver contract.DatabaseDriver, id uint32) (levelled, atCap int64, ok bool) {
	t.Helper()
	row, err := driver.FetchOne(context.Background(),
		`SELECT jobs_levelled_mask, jobs_at_cap_mask FROM characters WHERE id = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	var l, c sql.NullInt64
	if err := row.Scan(&l, &c); err != nil {
		t.Fatalf("scan masks of %d: %v", id, err)
	}
	if l.Valid != c.Valid {
		t.Fatalf("character %d has one mask set and one NULL: %v %v", id, l, c)
	}
	return l.Int64, c.Int64, l.Valid
}

func bits(ids ...uint) int64 {
	var m int64
	for _, id := range ids {
		m |= 1 << id
	}
	return m
}

func TestStoreResultWritesJobMasks(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver, repository.WithMaxLevel(100))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	result := contract.CensusResult{Characters: []contract.CharacterWrite{
		{
			Record: contract.CharacterRecord{ID: 3001, Name: "Folded", World: "Balmung", FirstSeenAt: now},
			Jobs: []contract.ClassJobRecord{
				{CharacterID: 3001, ClassJobID: 1, Name: "Gladiator", Level: 100},
				{CharacterID: 3001, ClassJobID: 26, Name: "Arcanist", Level: 42},
				{CharacterID: 3001, ClassJobID: 28, Name: "Scholar", Level: 42},
				{CharacterID: 3001, ClassJobID: 18, Name: "Fisher", Level: 99},
			},
		},
		{
			Record: contract.CharacterRecord{ID: 3002, Name: "Blue", World: "Balmung", FirstSeenAt: now},
			Jobs: []contract.ClassJobRecord{
				{CharacterID: 3002, ClassJobID: 36, Name: "Blue Mage", Level: 80},
				{CharacterID: 3002, ClassJobID: 25, Name: "Black Mage", Level: 80},
			},
		},
		{Record: contract.CharacterRecord{ID: 3003, Name: "Jobless", World: "Balmung", FirstSeenAt: now}},
	}}
	if err := repo.StoreResult(ctx, result); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id              uint32
		levelled, atCap int64
	}{
		{3001, bits(19, 27, 28, 18), bits(19)},
		{3002, bits(36, 25), bits(36)},
		{3003, 0, 0},
	}
	for _, c := range cases {
		levelled, atCap, ok := storedMasks(t, driver, c.id)
		if !ok || levelled != c.levelled || atCap != c.atCap {
			t.Errorf("character %d masks = %b, %b (set %v); want %b, %b", c.id, levelled, atCap, ok, c.levelled, c.atCap)
		}
	}

	// A re-census replaces both masks.
	if err := repo.Upsert(ctx, result.Characters[1].Record, []contract.ClassJobRecord{
		{CharacterID: 3002, ClassJobID: 25, Name: "Black Mage", Level: 100},
	}); err != nil {
		t.Fatal(err)
	}
	if levelled, atCap, _ := storedMasks(t, driver, 3002); levelled != bits(25) || atCap != bits(25) {
		t.Errorf("re-censused masks = %b, %b; want %b, %b", levelled, atCap, bits(25), bits(25))
	}
}

func TestStoreResultJobMasksUseTheConfiguredMaxLevel(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewCharacterRepository(driver, repository.WithMaxLevel(90))
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := repo.Upsert(context.Background(),
		contract.CharacterRecord{ID: 3101, Name: "Ninety", World: "Balmung", FirstSeenAt: now},
		[]contract.ClassJobRecord{{CharacterID: 3101, ClassJobID: 21, Name: "Warrior", Level: 90}}); err != nil {
		t.Fatal(err)
	}
	if _, atCap, _ := storedMasks(t, driver, 3101); atCap != bits(21) {
		t.Errorf("at-cap mask = %b, want %b", atCap, bits(21))
	}
}
