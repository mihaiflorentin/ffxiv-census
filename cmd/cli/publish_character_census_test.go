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

// seedTieredCharacters stores live characters in each census tier, censused
// the given time ago: active 101-105, recent 201-203, dormant 301-306.
func seedTieredCharacters(t *testing.T, now time.Time) *mockrepo.CharacterRepository {
	t.Helper()
	repo := mockrepo.NewCharacterFake()
	ctx := context.Background()
	seed := func(tier int, id uint32, ago time.Duration) {
		at := now.Add(-ago)
		if err := repo.Upsert(ctx, contract.CharacterRecord{ID: id, Name: "C", World: "W", FirstSeenAt: at, LastCensusAt: &at}, nil); err != nil {
			t.Fatal(err)
		}
		repo.SetCensusTier(id, tier)
	}
	for id, ago := range map[uint32]time.Duration{101: 4 * day, 102: 3 * day, 103: 2 * day, 104: time.Hour, 105: 5 * day} {
		seed(contract.CensusTierActive, id, ago)
	}
	for id, ago := range map[uint32]time.Duration{201: 5 * day, 202: 4 * day, 203: day} {
		seed(contract.CensusTierRecent, id, ago)
	}
	for i, id := range []uint32{301, 302, 303, 304, 305, 306} {
		seed(contract.CensusTierDormant, id, time.Duration(60-i)*day)
	}
	return repo
}

var testTierRevisit = []time.Duration{36 * time.Hour, 72 * time.Hour}

func TestPublishCharacterCensus_RevisitsTheActiveAndRecentTiersOnSchedule(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name  string
		limit int
		want  []uint32
	}{
		// Half of 10 for the upper tiers, split 3 and 2: the active
		// characters not censused for 36h (105, 101, 102 of the four due)
		// and the recent ones not censused for 72h (201, 202). The dormant
		// tier takes the other 5.
		{"due characters first, the rest to the dormant tier", 10, []uint32{105, 101, 102, 201, 202, 301, 302, 303, 304, 305}},
		// The dormant tier always keeps half of the limit.
		{"the dormant tier keeps its minimum share", 4, []uint32{105, 201, 301, 302}},
		// Both upper tiers have more due than their half of 4, so the
		// recent tier is never starved by the active one.
		{"the upper tiers split their part evenly", 8, []uint32{105, 101, 201, 202, 301, 302, 303, 304}},
		// The recent tier has only 2 due of its 3: the active tier takes
		// the spare.
		{"an upper tier's spare goes to the other", 12, []uint32{105, 101, 102, 103, 201, 202, 301, 302, 303, 304, 305, 306}},
		// The dormant tier has only 6: what it cannot use goes back to the
		// active tier, then the recent one, even before they are due.
		{"leftover goes back to the upper tiers", 14, []uint32{105, 101, 102, 103, 104, 201, 202, 203, 301, 302, 303, 304, 305, 306}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := seedTieredCharacters(t, now)
			q := &errorQueue{}
			opts := characterCensusOptions{Limit: tc.limit, TierRevisit: testTierRevisit, MinRestShare: 50}
			if err := publishCharacterCensus(context.Background(), repo, q, logger, now, opts); err != nil {
				t.Fatalf("publishCharacterCensus: %v", err)
			}
			if got := publishedCharacterIDs(t, q.jobs); !slices.Equal(got, tc.want) {
				t.Errorf("published = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPublishCharacterCensus_RejectsAnInvalidTierSchedule(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		revisit []time.Duration
		minRest int
	}{
		{[]time.Duration{36 * time.Hour}, 50},
		{[]time.Duration{36 * time.Hour, 72 * time.Hour, time.Hour}, 50},
		{[]time.Duration{0, 72 * time.Hour}, 50},
		{testTierRevisit, -1},
		{testTierRevisit, 101},
	} {
		q := &errorQueue{}
		opts := characterCensusOptions{Limit: 10, TierRevisit: tc.revisit, MinRestShare: tc.minRest}
		if err := publishCharacterCensus(context.Background(), seedTieredCharacters(t, now), q, logger, now, opts); err == nil {
			t.Errorf("revisit %v min rest %d published %d jobs, want an error", tc.revisit, tc.minRest, len(q.jobs))
		}
	}
}

func TestPublishCharacterCensus_SkipsTheRunWhileTheQueueIsBackedUp(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name              string
		depth, maxBacklog int
		wantPublished     bool
	}{
		// Still-queued jobs are the oldest characters, the ones the run would
		// select again: publishing now would census them twice.
		{"backlog at the limit", 5, 5, false},
		{"backlog above the limit", 50, 5, false},
		{"small backlog", 4, 5, true},
		{"guard disabled", 50, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &errorQueue{depth: tc.depth}
			opts := characterCensusOptions{Limit: 3, MaxBacklog: tc.maxBacklog, DeletedLimit: 10, DeletedOlderThan: 30 * day}
			if err := publishCharacterCensus(context.Background(), seedCensusCharacters(t, now), q, logger, now, opts); err != nil {
				t.Fatalf("publishCharacterCensus: %v", err)
			}
			if got := len(q.jobs) > 0; got != tc.wantPublished {
				t.Errorf("published %d jobs at depth %d with max backlog %d, want published=%v", len(q.jobs), tc.depth, tc.maxBacklog, tc.wantPublished)
			}
			if tc.maxBacklog > 0 && q.depthAsked != handler.EventCharacterCensus {
				t.Errorf("depth asked for %q, want %q", q.depthAsked, handler.EventCharacterCensus)
			}
		})
	}
}

func TestPublishCharacterCensus_FailsWhenTheBacklogCannotBeRead(t *testing.T) {
	now := time.Now().UTC()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &errorQueue{depthErr: errors.New("channel closed")}
	opts := characterCensusOptions{Limit: 3, MaxBacklog: 5}
	if err := publishCharacterCensus(context.Background(), seedCensusCharacters(t, now), q, logger, now, opts); err == nil || len(q.jobs) != 0 {
		t.Errorf("err=%v published=%d, want an error and nothing published", err, len(q.jobs))
	}
}
