package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// StoreResult is one database transaction in production: a failure leaves
// nothing of the result behind. The fake must behave the same.
func TestMockCharacterRepository_StoreResultIsAtomic(t *testing.T) {
	errBoom := errors.New("boom")
	latest := uint32(9)
	tests := []struct {
		name   string
		inject func(*CharacterRepository)
	}{
		{"upsert error", func(f *CharacterRepository) { f.UpsertErr = errBoom }},
		{"rejected character", func(f *CharacterRepository) { f.RejectID(2) }},
		{"rejected achievement", func(f *CharacterRepository) { f.RejectID(1) }},
		{"batch error", func(f *CharacterRepository) { f.UpsertBatchErr = errBoom }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewCharacterFake()
			ctx := context.Background()
			if err := repo.Upsert(ctx, contract.CharacterRecord{ID: 1, Name: "Before", FirstSeenAt: time.Now()}, nil); err != nil {
				t.Fatal(err)
			}
			tt.inject(repo)

			err := repo.StoreResult(ctx, contract.CensusResult{
				Characters: []contract.CharacterWrite{
					{Record: contract.CharacterRecord{ID: 3, Name: "New"}},
					{Record: contract.CharacterRecord{ID: 2, Name: "Two"}},
				},
				Achievements: []contract.AchievementWrite{{CharacterID: 1, LatestID: &latest}},
			})
			if err == nil {
				t.Fatal("StoreResult succeeded, want error")
			}
			for _, id := range []uint32{2, 3} {
				if got, _ := repo.Get(ctx, id); got != nil {
					t.Errorf("character %d stored despite a failed result: %+v", id, got)
				}
			}
			if got, _ := repo.Get(ctx, 1); got == nil || got.LatestAchievementID != nil {
				t.Errorf("achievement summary applied despite a failed result: %+v", got)
			}
			if len(repo.StoredAchievements) != 0 {
				t.Errorf("StoredAchievements = %+v, want none", repo.StoredAchievements)
			}
		})
	}
}
