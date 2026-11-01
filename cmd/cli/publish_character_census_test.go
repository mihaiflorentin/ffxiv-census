package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const day = 24 * time.Hour

// publishedCharacterIDs decodes the character IDs of published
// character-census jobs, in publish order.
func publishedCharacterIDs(t *testing.T, jobs []contract.QueueJob) []uint32 {
	t.Helper()
	ids := make([]uint32, 0, len(jobs))
	for _, j := range jobs {
		if j.Type != handler.EventCharacterCensus {
			t.Fatalf("job type = %q, want %q", j.Type, handler.EventCharacterCensus)
		}
		var p handler.CharacterCensusPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("payload unmarshal: %v", err)
		}
		ids = append(ids, p.CharacterID)
	}
	return ids
}

// seedCensusCharacters stores live characters 1-3 (last census 3, 2 and 1
// days ago), live character 4 (never censused) and deleted characters 10
// (deleted 90 days ago), 11 (45 days ago) and 12 (2 days ago).
func seedCensusCharacters(t *testing.T, now time.Time) *mockrepo.CharacterRepository {
	t.Helper()
	repo := mockrepo.NewCharacterFake()
	ctx := context.Background()
	for id, age := range map[uint32]time.Duration{1: 3 * day, 2: 2 * day, 3: day} {
		at := now.Add(-age)
		if err := repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "W", FirstSeenAt: at, LastCensusAt: &at}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Upsert(ctx, contract.CharacterRecord{ID: 4, Name: "C", World: "W", FirstSeenAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	for id, age := range map[uint32]time.Duration{10: 90 * day, 11: 45 * day, 12: 2 * day} {
		seen := now.Add(-100 * day)
		if err := repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "W", FirstSeenAt: seen, LastCensusAt: &seen}, nil); err != nil {
			t.Fatal(err)
		}
		if err := repo.MarkDeleted(ctx, id, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestPublishCharacterCensus_PublishesLiveCharactersOldestFirstAndSkipsDeleted(t *testing.T) {
	now := time.Now().UTC()
	repo := seedCensusCharacters(t, now)
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	opts := characterCensusOptions{Limit: 300}
	if err := publishCharacterCensus(context.Background(), repo, q, logger, now, opts); err != nil {
		t.Fatalf("publishCharacterCensus: %v", err)
	}

	want := []uint32{4, 1, 2, 3} // never censused first, then oldest census; no deleted characters
	if got := publishedCharacterIDs(t, q.jobs); !slices.Equal(got, want) {
		t.Errorf("published = %v, want %v", got, want)
	}
}

func TestPublishCharacterCensus_ReprobesDueDeletedCharactersInABoundedBatch(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		opts characterCensusOptions
		want []uint32
	}{
		{
			name: "due deleted characters follow the live ones, longest-deleted first",
			opts: characterCensusOptions{Limit: 300, DeletedLimit: 10, DeletedOlderThan: 30 * day},
			want: []uint32{4, 1, 2, 3, 10, 11},
		},
		{
			name: "the batch is bounded by the deleted limit",
			opts: characterCensusOptions{Limit: 300, DeletedLimit: 1, DeletedOlderThan: 30 * day},
			want: []uint32{4, 1, 2, 3, 10},
		},
		{
			name: "a deleted limit of zero disables the re-probe",
			opts: characterCensusOptions{Limit: 300, DeletedLimit: 0, DeletedOlderThan: 30 * day},
			want: []uint32{4, 1, 2, 3},
		},
		{
			name: "a longer re-probe interval leaves recently deleted characters alone",
			opts: characterCensusOptions{Limit: 300, DeletedLimit: 10, DeletedOlderThan: 60 * day},
			want: []uint32{4, 1, 2, 3, 10},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := seedCensusCharacters(t, now)
			q := &errorQueue{}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))

			if err := publishCharacterCensus(context.Background(), repo, q, logger, now, tt.opts); err != nil {
				t.Fatalf("publishCharacterCensus: %v", err)
			}
			if got := publishedCharacterIDs(t, q.jobs); !slices.Equal(got, tt.want) {
				t.Errorf("published = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPublishCharacterCensus_SelectionErrorPublishesNothing(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := characterCensusOptions{Limit: 300, DeletedLimit: 10, DeletedOlderThan: 30 * day}

	tests := map[string]func(*mockrepo.CharacterRepository){
		"stale selection":   func(r *mockrepo.CharacterRepository) { r.ListStaleErr = errors.New("boom") },
		"deleted selection": func(r *mockrepo.CharacterRepository) { r.ListDeletedErr = errors.New("boom") },
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			repo := seedCensusCharacters(t, now)
			inject(repo)
			q := &errorQueue{}
			if err := publishCharacterCensus(context.Background(), repo, q, logger, now, opts); err == nil {
				t.Fatal("expected error")
			}
			if len(q.jobs) != 0 {
				t.Errorf("published %d jobs after a selection error, want 0", len(q.jobs))
			}
		})
	}
}

func TestPublishCharacterCensusCmd_DeletedReprobeFlagDefaults(t *testing.T) {
	flags := publishCharacterCensusCmd.Flags()

	limit, err := flags.GetInt("deleted-limit")
	if err != nil {
		t.Fatalf("GetInt deleted-limit: %v", err)
	}
	if limit != 10 {
		t.Errorf("deleted-limit default = %d, want 10", limit)
	}
	age, err := flags.GetDuration("deleted-older-than")
	if err != nil {
		t.Fatalf("GetDuration deleted-older-than: %v", err)
	}
	if age != 720*time.Hour {
		t.Errorf("deleted-older-than default = %v, want 720h", age)
	}
}

func TestTierQuotasSplitTheLimitByShare(t *testing.T) {
	for _, tc := range []struct {
		limit  int
		shares []int
		want   []int
	}{
		{1200, []int{30, 20, 50}, []int{360, 240, 600}},
		{10, []int{30, 20, 50}, []int{3, 2, 5}},
		// Rounding leftovers go to the last tier, so the quotas sum to limit.
		{7, []int{30, 20, 50}, []int{2, 1, 4}},
		// Shares are relative, not percentages.
		{6, []int{1, 1, 1}, []int{2, 2, 2}},
	} {
		if got := tierQuotas(tc.limit, tc.shares); !slices.Equal(got, tc.want) {
			t.Errorf("tierQuotas(%d, %v) = %v, want %v", tc.limit, tc.shares, got, tc.want)
		}
	}
}

// seedTieredCharacters stores live characters in each census tier, every one
// censused a different number of days ago: tier 1 (public) 101-104, tier 2
// 201-202, tier 3 301-306. A higher id within a tier was censused more
// recently.
func seedTieredCharacters(t *testing.T, now time.Time) *mockrepo.CharacterRepository {
	t.Helper()
	repo := mockrepo.NewCharacterFake()
	ctx := context.Background()
	seed := func(tier int, ids ...uint32) {
		for i, id := range ids {
			at := now.Add(-time.Duration(len(ids)-i) * day)
			rec := contract.CharacterRecord{ID: id, Name: "C", World: "W", FirstSeenAt: at, LastCensusAt: &at, AchievementsPrivate: tier != contract.CensusTierPublic}
			if err := repo.Upsert(ctx, rec, nil); err != nil {
				t.Fatal(err)
			}
			repo.SetCensusTier(id, tier)
		}
	}
	seed(contract.CensusTierPublic, 101, 102, 103, 104)
	seed(contract.CensusTierActivePrivate, 201, 202)
	seed(contract.CensusTierPrivate, 301, 302, 303, 304, 305, 306)
	return repo
}

func TestPublishCharacterCensus_SplitsTheLimitAcrossCensusTiers(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name  string
		limit int
		want  []uint32
	}{
		// 30/20/50 of 10: 3 public, 2 active private, 5 other private.
		{"every tier fills its share", 10, []uint32{101, 102, 103, 201, 202, 301, 302, 303, 304, 305}},
		// 30/20/50 of 20: 6, 4 and 10. Tier 1 has 4 and tier 2 has 2, so
		// their 4 left over go to tier 3, which has only 6: the 4 that remain
		// go back to the earlier tiers, and none has more.
		{"leftover moves to the other tiers", 20, []uint32{101, 102, 103, 104, 201, 202, 301, 302, 303, 304, 305, 306}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := seedTieredCharacters(t, now)
			q := &errorQueue{}
			opts := characterCensusOptions{Limit: tc.limit, TierShares: []int{30, 20, 50}}
			if err := publishCharacterCensus(context.Background(), repo, q, logger, now, opts); err != nil {
				t.Fatalf("publishCharacterCensus: %v", err)
			}
			if got := publishedCharacterIDs(t, q.jobs); !slices.Equal(got, tc.want) {
				t.Errorf("published = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPublishCharacterCensus_LeftoverFromALaterTierGoesToAnEarlierOne(t *testing.T) {
	now := time.Now().UTC()
	repo := mockrepo.NewCharacterFake()
	ctx := context.Background()
	for i, id := range []uint32{101, 102, 103, 104, 105, 106} {
		at := now.Add(-time.Duration(6-i) * day)
		if err := repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "W", FirstSeenAt: at, LastCensusAt: &at}, nil); err != nil {
			t.Fatal(err)
		}
	}
	q := &errorQueue{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Only public characters: tiers 2 and 3 are empty, so the public tier
	// takes the whole limit of 5.
	opts := characterCensusOptions{Limit: 5, TierShares: []int{30, 20, 50}}
	if err := publishCharacterCensus(ctx, repo, q, logger, now, opts); err != nil {
		t.Fatalf("publishCharacterCensus: %v", err)
	}
	if got, want := publishedCharacterIDs(t, q.jobs), []uint32{101, 102, 103, 104, 105}; !slices.Equal(got, want) {
		t.Errorf("published = %v, want %v", got, want)
	}
}

func TestPublishCharacterCensus_RejectsSharesThatLeaveATierOut(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, shares := range [][]int{{30, 70}, {30, 20, 50, 10}, {30, 0, 70}, {-10, 60, 50}} {
		q := &errorQueue{}
		opts := characterCensusOptions{Limit: 10, TierShares: shares}
		if err := publishCharacterCensus(context.Background(), seedTieredCharacters(t, now), q, logger, now, opts); err == nil {
			t.Errorf("shares %v published %d jobs, want an error", shares, len(q.jobs))
		}
	}
}
