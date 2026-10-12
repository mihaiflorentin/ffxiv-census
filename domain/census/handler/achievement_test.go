package handler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/mock"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func newTestAchievementCensus(t *testing.T) (*AchievementCensus, *mocklodestone.Fake, *mockrepo.CharacterRepository, *mockrepo.AchievementRepository) {
	t.Helper()
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	svc := census.NewService(chars, ach, mockrepo.NewCensusRunFake())
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	return NewAchievementCensus(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, ach)), ls, chars, ach
}

func achievementPayload(characterID uint32) []byte {
	b, _ := json.Marshal(AchievementCensusPayload{CharacterID: characterID})
	return b
}

func TestAchievementCensus_Processes(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 123, Name: "X", FirstSeenAt: time.Now()}, nil)

	now := time.Now()
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{
			Milestones: []contract.AchievementResult{
				{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: now.Add(-time.Hour)},
			},
			LatestAchievement: &contract.AchievementResult{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: now},
		}, nil
	}

	next, err := h.Handle(context.Background(), achievementPayload(123))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next jobs = %d, want 0 (leaf event)", len(next))
	}
	got, _ := chars.Get(context.Background(), 123)
	if got.LatestAchievementID == nil || *got.LatestAchievementID != 590 {
		t.Errorf("latest achievement = %v, want 590", got.LatestAchievementID)
	}
	// Only the registered milestone (590) is recorded.
	milestones, err := ach.ListCharacterMilestones(context.Background(), 123)
	if err != nil {
		t.Fatalf("ListCharacterMilestones: %v", err)
	}
	if len(milestones) != 1 || milestones[0].AchievementID != 590 {
		t.Errorf("milestones = %+v, want only 590", milestones)
	}
}

func TestAchievementCensus_FetchError(t *testing.T) {
	h, ls, _, _ := newTestAchievementCensus(t)
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		return nil, errors.New("boom")
	}
	if _, err := h.Handle(context.Background(), achievementPayload(1)); err == nil {
		t.Fatal("expected error on fetch failure")
	}
}

func TestAchievementCensus_WaitsForRateLimitedLodestone(t *testing.T) {
	ls := mocklodestone.NewFake()
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	rl := mock.NewProviderRateLimiter()
	rl.Pause(contract.ProviderLodestone, 100*time.Millisecond, "test pause")

	var fetched bool
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		fetched = true
		return &contract.AchievementSummary{}, nil
	}

	h := NewAchievementCensus(ls, svc, nil, rl).WithWriter(mockwriter.NewFake())
	start := time.Now()
	_, err := h.Handle(context.Background(), achievementPayload(1))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !fetched {
		t.Fatal("FetchAchievements was not called after wait")
	}
	if elapsed < 90*time.Millisecond {
		t.Errorf("Handle returned too quickly (%v), expected wait ~100ms", elapsed)
	}
}

func TestAchievementCensus_SkipsWhenAllMilestonesKnown(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	now := time.Now()
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  200,
		Name:                "Fresh",
		FirstSeenAt:         now,
		LatestAchievementAt: &now, // Fresh data
	}, nil)

	// Pre-seed all 7 milestones.
	allMilestones := []contract.CharacterMilestone{
		{CharacterID: 200, AchievementID: 590, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 1129, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 1139, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 1794, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 2298, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 2958, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 200, AchievementID: 3496, AchievedAt: now.Add(-48 * time.Hour)},
	}
	if err := ach.UpsertCharacterMilestones(context.Background(), 200, allMilestones); err != nil {
		t.Fatalf("seed milestones: %v", err)
	}

	var fetched bool
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		fetched = true
		return &contract.AchievementSummary{}, nil
	}

	next, err := h.Handle(context.Background(), achievementPayload(200))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next jobs = %d, want 0", len(next))
	}
	if !fetched {
		t.Error("FetchAchievements was not called to refresh global latest activity")
	}
}

func TestAchievementCensus_AllKnownOldAchievementsDoNotRefetch(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	now := time.Now()
	stale := now.Add(-10 * 24 * time.Hour) // 10 days old > 7-day threshold
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  300,
		Name:                "Stale",
		FirstSeenAt:         now,
		LatestAchievementAt: &stale,
	}, nil)

	// Pre-seed all 7 milestones.
	allMilestones := []contract.CharacterMilestone{
		{CharacterID: 300, AchievementID: 590, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 1129, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 1139, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 1794, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 2298, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 2958, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 300, AchievementID: 3496, AchievedAt: now.Add(-48 * time.Hour)},
	}
	if err := ach.UpsertCharacterMilestones(context.Background(), 300, allMilestones); err != nil {
		t.Fatalf("seed milestones: %v", err)
	}

	var fetched bool
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		fetched = true
		return &contract.AchievementSummary{}, nil
	}

	_, err := h.Handle(context.Background(), achievementPayload(300))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !fetched {
		t.Error("FetchAchievements was not called to refresh global latest activity")
	}
}

func TestAchievementCensus_RequestsOnlyMissingMilestonesInOrder(t *testing.T) {
	tests := []struct {
		name          string
		characterID   uint32
		known         []contract.CharacterMilestone
		wantRequested []uint32
		wantCalls     int
	}{
		{"nothing known starts from chocobo", 501, nil, []uint32{590, 1129, 1139, 1794, 2298, 2958, 3496}, 1},
		{"known prefix requests missing checkpoints", 502, []contract.CharacterMilestone{{AchievementID: 590}, {AchievementID: 1129}, {AchievementID: 1139}}, []uint32{1794, 2298, 2958, 3496}, 1},
		{"later known checkpoints leave only early hole", 504, []contract.CharacterMilestone{{AchievementID: 1129}, {AchievementID: 1139}, {AchievementID: 1794}, {AchievementID: 2298}, {AchievementID: 2958}, {AchievementID: 3496}}, []uint32{590}, 1},
		{"complete history refreshes list with no details", 503, []contract.CharacterMilestone{{AchievementID: 590}, {AchievementID: 1129}, {AchievementID: 1139}, {AchievementID: 1794}, {AchievementID: 2298}, {AchievementID: 2958}, {AchievementID: 3496}}, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ls, chars, ach := newTestAchievementCensus(t)
			now := time.Now()
			if err := chars.Upsert(context.Background(), contract.CharacterRecord{ID: tt.characterID, Name: "X", FirstSeenAt: now}, nil); err != nil {
				t.Fatal(err)
			}
			for i := range tt.known {
				tt.known[i].CharacterID = tt.characterID
				tt.known[i].AchievedAt = now
			}
			if err := ach.UpsertCharacterMilestones(context.Background(), tt.characterID, tt.known); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var got []uint32
			ls.FetchAchievementsFunc = func(_ context.Context, _ uint32, ids []uint32) (*contract.AchievementSummary, error) {
				calls++
				got = append([]uint32(nil), ids...)
				return &contract.AchievementSummary{}, nil
			}
			if _, err := h.Handle(context.Background(), achievementPayload(tt.characterID)); err != nil {
				t.Fatal(err)
			}
			if calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tt.wantCalls)
			}
			if len(got) != len(tt.wantRequested) {
				t.Fatalf("requested = %v, want %v", got, tt.wantRequested)
			}
			for i := range got {
				if got[i] != tt.wantRequested[i] {
					t.Fatalf("requested = %v, want %v", got, tt.wantRequested)
				}
			}
		})
	}
}

func TestAchievementCensus_UsesGlobalLatestAchievement(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	older, later := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	if err := chars.Upsert(context.Background(), contract.CharacterRecord{ID: 505, Name: "X", FirstSeenAt: older}, nil); err != nil {
		t.Fatal(err)
	}
	known := []contract.CharacterMilestone{{CharacterID: 505, AchievementID: 3496, AchievedAt: later}}
	if err := ach.UpsertCharacterMilestones(context.Background(), 505, known); err != nil {
		t.Fatal(err)
	}
	ls.FetchAchievementsFunc = func(_ context.Context, _ uint32, ids []uint32) (*contract.AchievementSummary, error) {
		if len(ids) != 6 || ids[0] != 590 {
			t.Fatalf("requested = %v, want missing IDs", ids)
		}
		return &contract.AchievementSummary{Milestones: []contract.AchievementResult{{AchievementID: 590, Earned: true, EarnedAt: older}}, LatestAchievement: &contract.AchievementResult{AchievementID: 999, Earned: true, EarnedAt: later}}, nil
	}
	if _, err := h.Handle(context.Background(), achievementPayload(505)); err != nil {
		t.Fatal(err)
	}
	got, err := chars.Get(context.Background(), 505)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestAchievementID == nil || *got.LatestAchievementID != 999 {
		t.Fatalf("latest ID = %v, want 999", got.LatestAchievementID)
	}
	if got.LatestAchievementAt == nil || !got.LatestAchievementAt.Equal(later) {
		t.Fatalf("latest at = %v, want %v", got.LatestAchievementAt, later)
	}
	milestones, err := ach.ListCharacterMilestones(context.Background(), 505)
	if err != nil {
		t.Fatal(err)
	}
	persisted := make(map[uint32]bool, len(milestones))
	for _, milestone := range milestones {
		persisted[milestone.AchievementID] = true
	}
	if len(persisted) != 2 || !persisted[590] || !persisted[3496] {
		t.Fatalf("persisted milestones = %v, want 590 and 3496", persisted)
	}
}

func TestAchievementCensus_ScrapesWhenMilestonesMissing(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	now := time.Now()
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{
		ID:                  400,
		Name:                "Partial",
		FirstSeenAt:         now,
		LatestAchievementAt: &now,
	}, nil)

	// Pre-seed only 3 of 7 milestones.
	partialMilestones := []contract.CharacterMilestone{
		{CharacterID: 400, AchievementID: 590, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 400, AchievementID: 1129, AchievedAt: now.Add(-48 * time.Hour)},
		{CharacterID: 400, AchievementID: 1139, AchievedAt: now.Add(-48 * time.Hour)},
	}
	if err := ach.UpsertCharacterMilestones(context.Background(), 400, partialMilestones); err != nil {
		t.Fatalf("seed milestones: %v", err)
	}

	var fetched bool
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		fetched = true
		return &contract.AchievementSummary{
			Milestones: []contract.AchievementResult{
				{AchievementID: 1794, Name: "Stormblood", Earned: true, EarnedAt: now.Add(-time.Hour)},
				{AchievementID: 2298, Name: "Shadowbringers", Earned: true, EarnedAt: now.Add(-time.Hour)},
				{AchievementID: 2958, Name: "Endwalker", Earned: true, EarnedAt: now.Add(-time.Hour)},
				{AchievementID: 3496, Name: "Dawntrail", Earned: true, EarnedAt: now},
			},
			LatestAchievement: &contract.AchievementResult{AchievementID: 3496, Name: "Dawntrail", Earned: true, EarnedAt: now},
		}, nil
	}

	_, err := h.Handle(context.Background(), achievementPayload(400))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !fetched {
		t.Error("FetchAchievements was not called when milestones are missing")
	}
}

func TestAchievementCensus_CharacterNotFoundMarksDeleted(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 410, Name: "Doomed Character", FirstSeenAt: time.Now()}, nil)
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		return nil, contract.ErrCharacterNotFound
	}

	next, err := h.Handle(context.Background(), achievementPayload(410))
	if err != nil {
		t.Fatalf("a genuine Lodestone 404 is terminal and must ack the job, got %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("expected no chained jobs for a deleted character, got %d", len(next))
	}
	got, gerr := chars.Get(context.Background(), 410)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if got == nil || got.DeletedAt == nil {
		t.Fatalf("character 410 must be marked deleted, got %+v", got)
	}
}

func TestAchievementCensus_DecodeErrorIsPoison(t *testing.T) {
	h, _, _, _ := newTestAchievementCensus(t)
	_, err := h.Handle(context.Background(), []byte("{not json"))
	if !errors.Is(err, contract.ErrPoisonPayload) {
		t.Fatalf("undecodable payload must wrap contract.ErrPoisonPayload, got %v", err)
	}
}

// seedPrivate stores a character whose achievement census found it private at
// checkedAt, through the same write path production uses.
func seedPrivate(t *testing.T, chars *mockrepo.CharacterRepository, id uint32, checkedAt time.Time) {
	t.Helper()
	err := chars.StoreResult(context.Background(), contract.CensusResult{
		Characters:   []contract.CharacterWrite{{Record: contract.CharacterRecord{ID: id, Name: "Private", FirstSeenAt: time.Now()}}},
		Achievements: []contract.AchievementWrite{{CharacterID: id, Private: true, CheckedAt: checkedAt}},
	})
	if err != nil {
		t.Fatalf("seed private character: %v", err)
	}
}

// countFetches makes the fake Lodestone answer every achievement request with
// summary and returns a pointer to the number of requests it received.
func countFetches(ls *mocklodestone.Fake, summary *contract.AchievementSummary) *int {
	var n int
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		n++
		return summary, nil
	}
	return &n
}

func TestAchievementCensus_PrivateCheckedWithinWindowMakesNoLodestoneRequest(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	seedPrivate(t, chars, 700, time.Now().Add(-29*24*time.Hour))
	fetches := countFetches(ls, &contract.AchievementSummary{Private: true})

	next, err := h.Handle(context.Background(), achievementPayload(700))
	if err != nil {
		t.Fatalf("a skipped job must succeed so the worker acks it, got %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next jobs = %d, want 0", len(next))
	}
	if *fetches != 0 {
		t.Fatalf("Lodestone requests = %d, want 0 for a private character checked 29 days ago", *fetches)
	}
}

func TestAchievementCensus_PrivateCheckedBeforeWindowIsCheckedAgain(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	seedPrivate(t, chars, 701, time.Now().Add(-31*24*time.Hour))
	fetches := countFetches(ls, &contract.AchievementSummary{Private: true})

	if _, err := h.Handle(context.Background(), achievementPayload(701)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if *fetches != 1 {
		t.Fatalf("Lodestone requests = %d, want 1 once the 30-day window has passed", *fetches)
	}
	got, _ := chars.Get(context.Background(), 701)
	if got.AchievementsCheckedAt == nil || time.Since(*got.AchievementsCheckedAt) > time.Minute {
		t.Fatalf("achievements_checked_at = %v, want it refreshed to now", got.AchievementsCheckedAt)
	}
}

func TestAchievementCensus_UnknownFlagIsCheckedOnceThenFollowsTheRule(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 702, Name: "New", FirstSeenAt: time.Now()}, nil)
	fetches := countFetches(ls, &contract.AchievementSummary{Private: true})

	for i := 0; i < 3; i++ {
		if _, err := h.Handle(context.Background(), achievementPayload(702)); err != nil {
			t.Fatalf("Handle #%d: %v", i+1, err)
		}
	}
	if *fetches != 1 {
		t.Fatalf("Lodestone requests = %d, want 1: checked once, then skipped", *fetches)
	}
}

func TestAchievementCensus_PrivateFlagWithoutCheckTimeIsStampedWithoutALodestoneRequest(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	// A flag stored before checks were timestamped: private, never stamped.
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 703, Name: "Legacy", AchievementsPrivate: true, FirstSeenAt: time.Now()}, nil)
	fetches := countFetches(ls, &contract.AchievementSummary{Private: true})

	if _, err := h.Handle(context.Background(), achievementPayload(703)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if *fetches != 0 {
		t.Fatalf("Lodestone requests = %d, want 0: the flag is trusted and only stamped", *fetches)
	}
	got, _ := chars.Get(context.Background(), 703)
	if got == nil || !got.AchievementsPrivate || got.AchievementsCheckedAt == nil || time.Since(*got.AchievementsCheckedAt) > time.Minute {
		t.Fatalf("character = %+v, want it still private and stamped now", got)
	}
	stamp := *got.AchievementsCheckedAt

	// The second job is skipped without another write.
	if _, err := h.Handle(context.Background(), achievementPayload(703)); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if *fetches != 0 {
		t.Fatalf("Lodestone requests after the second job = %d, want 0", *fetches)
	}
	if again, _ := chars.Get(context.Background(), 703); !again.AchievementsCheckedAt.Equal(stamp) {
		t.Fatalf("stamp rewritten by the second job: %v, was %v", again.AchievementsCheckedAt, stamp)
	}

	// Thirty-one days after the first sighting it is verified for real.
	seedPrivate(t, chars, 703, time.Now().Add(-31*24*time.Hour))
	if _, err := h.Handle(context.Background(), achievementPayload(703)); err != nil {
		t.Fatalf("third Handle: %v", err)
	}
	if *fetches != 1 {
		t.Fatalf("Lodestone requests after 31 days = %d, want 1", *fetches)
	}
}

func TestAchievementCensus_NonPrivateCharactersAreCheckedEveryTime(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 704, Name: "Public", FirstSeenAt: time.Now()}, nil)
	fetches := countFetches(ls, &contract.AchievementSummary{})

	for i := 0; i < 3; i++ {
		if _, err := h.Handle(context.Background(), achievementPayload(704)); err != nil {
			t.Fatalf("Handle #%d: %v", i+1, err)
		}
	}
	if *fetches != 3 {
		t.Fatalf("Lodestone requests = %d, want 3: only private characters are skipped", *fetches)
	}
}

func TestAchievementCensus_RecheckWindowIsConfigurable(t *testing.T) {
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	svc := census.NewService(chars, ach, mockrepo.NewCensusRunFake())
	svc.SetPrivateRecheckWindow(7 * 24 * time.Hour)
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	h := NewAchievementCensus(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, ach))
	seedPrivate(t, chars, 705, time.Now().Add(-8*24*time.Hour))
	seedPrivate(t, chars, 706, time.Now().Add(-6*24*time.Hour))
	fetches := countFetches(ls, &contract.AchievementSummary{Private: true})

	for _, id := range []uint32{705, 706} {
		if _, err := h.Handle(context.Background(), achievementPayload(id)); err != nil {
			t.Fatalf("Handle %d: %v", id, err)
		}
	}
	if *fetches != 1 {
		t.Fatalf("Lodestone requests = %d, want 1 (only the character checked 8 days ago, window 7 days)", *fetches)
	}
}

func TestAchievementCensus_UncensusedCharacterIsStillChecked(t *testing.T) {
	h, ls, _, _ := newTestAchievementCensus(t)
	fetches := countFetches(ls, &contract.AchievementSummary{})

	if _, err := h.Handle(context.Background(), achievementPayload(799)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if *fetches != 1 {
		t.Fatalf("Lodestone requests = %d, want 1 for a character the census has no row for", *fetches)
	}
}
