package repository

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestMockCharacterRepository_IDSweepCursor(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()
	if err := repo.Upsert(ctx, contract.CharacterRecord{ID: 42, FirstSeenAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := repo.IDSweepCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 43 {
		t.Fatalf("initial cursor = %d, want 43", got)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 43, 143); err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceIDSweepCursor(ctx, 43, 100); err != nil {
		t.Fatalf("stale covered advance should be idempotent: %v", err)
	}
	got, err = repo.IDSweepCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != 143 {
		t.Fatalf("cursor = %d, want 143", got)
	}
}

func TestMockCharacterRepository_ListFilter(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()
	seed := func(id uint32, world, dc, region, race, name string) {
		rec := contract.CharacterRecord{ID: id, Name: name, World: world, Datacenter: dc, Region: region, Race: race, FirstSeenAt: time.Now().UTC()}
		if err := repo.Upsert(ctx, rec, nil); err != nil {
			t.Fatalf("Upsert %d: %v", id, err)
		}
	}
	seed(1, "Louisoix", "Chaos", "EU", "Au Ra", "Feed How")
	seed(2, "Louisoix", "Chaos", "EU", "Miqo'te", "Ninto Thegen")
	seed(3, "Zodiark", "Light", "EU", "Miqo'te", "Ahribella White")
	seed(4, "Ultros", "Primal", "NA", "Hyur", "Alpha Test")

	cases := []struct {
		name   string
		filter contract.CharacterFilter
		want   []uint32
	}{
		{"world exact", contract.CharacterFilter{World: "Louisoix"}, []uint32{1, 2}},
		{"race exact", contract.CharacterFilter{Race: "Miqo'te"}, []uint32{2, 3}},
		{"name substring case-insensitive", contract.CharacterFilter{Name: "feed"}, []uint32{1}},
		{"combined AND", contract.CharacterFilter{World: "Louisoix", Race: "Miqo'te"}, []uint32{2}},
		{"no match", contract.CharacterFilter{World: "Balmung"}, nil},
		{"empty filter returns all", contract.CharacterFilter{}, []uint32{1, 2, 3, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.List(ctx, tc.filter, 10, 0)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			var ids []uint32
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("ids = %v, want %v", ids, tc.want)
			}
			for i := range ids {
				if ids[i] != tc.want[i] {
					t.Fatalf("ids = %v, want %v", ids, tc.want)
				}
			}
			n, err := repo.Count(ctx, tc.filter)
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			if n != int64(len(tc.want)) {
				t.Fatalf("Count = %d, want %d", n, len(tc.want))
			}
		})
	}
}

func TestMockCharacterRepository_GearAndGaps(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()
	now := time.Now().UTC()

	dye := "Jet Black"
	gear := []contract.CharacterGearRecord{
		{
			CharacterID: 50,
			Slot:        "MainHand",
			ItemID:      1234,
			Name:        "Test Sword",
			ItemLevel:   660,
			Dye:         &dye,
			Materia:     []string{"Materia A"},
			UpdatedAt:   now,
		},
	}

	if err := repo.UpsertGear(ctx, 50, gear); err != nil {
		t.Fatalf("UpsertGear: %v", err)
	}
	gotGear, err := repo.GetGear(ctx, 50)
	if err != nil {
		t.Fatalf("GetGear: %v", err)
	}
	if len(gotGear) != 1 || gotGear[0].Name != "Test Sword" || gotGear[0].Dye == nil || *gotGear[0].Dye != dye {
		t.Fatalf("GetGear mismatch: %+v", gotGear)
	}

	// Test FindIDGapsAfter (from after the cursor at 6)
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 3, FirstSeenAt: now}, nil)
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 4, FirstSeenAt: now}, nil)
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 8, FirstSeenAt: now}, nil)
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 15, FirstSeenAt: now}, nil)

	gaps, err := repo.FindIDGapsAfter(ctx, 6, 15, 10)
	if err != nil {
		t.Fatalf("FindIDGapsAfter: %v", err)
	}
	want := [][2]uint32{
		{7, 7},
		{9, 14},
	}
	if len(gaps) != len(want) {
		t.Fatalf("gaps = %v, want %v", gaps, want)
	}
	for i := range gaps {
		if gaps[i] != want[i] {
			t.Errorf("gap[%d] = %v, want %v", i, gaps[i], want[i])
		}
	}
}

func TestMockCharacterRepository_MinLevelFilter(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()
	now := time.Now().UTC()

	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 1, Name: "Char 1", FirstSeenAt: now}, []contract.ClassJobRecord{
		{CharacterID: 1, Level: 100},
	})
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 2, Name: "Char 2", FirstSeenAt: now}, []contract.ClassJobRecord{
		{CharacterID: 2, Level: 90},
	})
	_ = repo.Upsert(ctx, contract.CharacterRecord{ID: 3, Name: "Char 3", FirstSeenAt: now}, []contract.ClassJobRecord{
		{CharacterID: 3, Level: 50},
	})

	count100, err := repo.Count(ctx, contract.CharacterFilter{MinLevel: 100})
	if err != nil {
		t.Fatalf("Count(MinLevel: 100): %v", err)
	}
	if count100 != 1 {
		t.Errorf("Count(MinLevel: 100) = %d, want 1", count100)
	}

	count90, err := repo.Count(ctx, contract.CharacterFilter{MinLevel: 90})
	if err != nil {
		t.Fatalf("Count(MinLevel: 90): %v", err)
	}
	if count90 != 2 {
		t.Errorf("Count(MinLevel: 90) = %d, want 2", count90)
	}

	list100, err := repo.List(ctx, contract.CharacterFilter{MinLevel: 100}, 10, 0)
	if err != nil {
		t.Fatalf("List(MinLevel: 100): %v", err)
	}
	if len(list100) != 1 || list100[0].ID != 1 {
		t.Errorf("List(MinLevel: 100) = %v, want [ID: 1]", list100)
	}
}

func TestMockCharacterRepository_ListStaleIDs(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()

	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)

	// id=1: NULL last_census_at
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "NullCensus", World: "Balmung", FirstSeenAt: old,
	}, nil)
	// id=2: old last_census_at
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 2, Name: "OldCensus", World: "Balmung", FirstSeenAt: old, LastCensusAt: &old,
	}, nil)
	// id=3: recent last_census_at
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 3, Name: "RecentCensus", World: "Balmung", FirstSeenAt: old, LastCensusAt: &recent,
	}, nil)
	// id=4: oldest census but deleted: never selected.
	_ = repo.Upsert(ctx, contract.CharacterRecord{
		ID: 4, Name: "Gone", World: "Balmung", FirstSeenAt: old, LastCensusAt: &old,
	}, nil)
	_ = repo.MarkDeleted(ctx, 4, recent)

	// Zero cutoff, limit 2: NULL first, then oldest.
	got, err := repo.ListStaleIDs(ctx, time.Time{}, 2)
	if err != nil {
		t.Fatalf("ListStaleIDs zero cutoff: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2}) {
		t.Errorf("zero cutoff limit 2 = %v, want [1 2]", got)
	}

	// Positive cutoff at recent: excludes id=3 (recent.Before(recent) is false).
	got, err = repo.ListStaleIDs(ctx, recent, 10)
	if err != nil {
		t.Fatalf("ListStaleIDs positive cutoff: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2}) {
		t.Errorf("positive cutoff = %v, want [1 2]", got)
	}

	// Zero cutoff, limit 10: every live character.
	got, err = repo.ListStaleIDs(ctx, time.Time{}, 10)
	if err != nil {
		t.Fatalf("ListStaleIDs zero cutoff all: %v", err)
	}
	if !slices.Equal(got, []uint32{1, 2, 3}) {
		t.Errorf("zero cutoff all = %v, want [1 2 3]", got)
	}

	repo.ListStaleErr = errors.New("boom")
	if _, err := repo.ListStaleIDs(ctx, time.Time{}, 10); err == nil {
		t.Error("ListStaleIDs ignored the injected error")
	}
}

func TestMockCharacterRepository_ListDeletedIDs(t *testing.T) {
	repo := NewCharacterFake()
	ctx := context.Background()

	seen := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	deleteAt := map[uint32]time.Time{
		10: cutoff.Add(-48 * time.Hour),
		11: cutoff.Add(-24 * time.Hour),
		12: cutoff.Add(-72 * time.Hour),
		13: cutoff,                     // exactly at the cutoff: not yet due
		14: cutoff.Add(24 * time.Hour), // after the cutoff: not yet due
	}
	for id := uint32(10); id <= 15; id++ {
		_ = repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "Balmung", FirstSeenAt: seen}, nil)
		if at, ok := deleteAt[id]; ok {
			_ = repo.MarkDeleted(ctx, id, at)
		}
	}

	got, err := repo.ListDeletedIDs(ctx, cutoff, 10)
	if err != nil {
		t.Fatalf("ListDeletedIDs: %v", err)
	}
	if !slices.Equal(got, []uint32{12, 10, 11}) {
		t.Errorf("due deleted = %v, want [12 10 11]", got)
	}

	got, _ = repo.ListDeletedIDs(ctx, cutoff, 2)
	if !slices.Equal(got, []uint32{12, 10}) {
		t.Errorf("bounded batch = %v, want [12 10]", got)
	}

	got, _ = repo.ListDeletedIDs(ctx, cutoff, 0)
	if len(got) != 0 {
		t.Errorf("zero limit = %v, want none", got)
	}

	repo.ListDeletedErr = errors.New("boom")
	if _, err := repo.ListDeletedIDs(ctx, cutoff, 10); err == nil {
		t.Error("ListDeletedIDs ignored the injected error")
	}
}
