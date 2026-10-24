package census

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func newTestService(t *testing.T) (*Service, *mockrepo.CharacterRepository) {
	t.Helper()
	chars := mockrepo.NewCharacterFake()
	svc := NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	return svc, chars
}

// newTestServiceAll returns the service plus every fake it depends on, so
// tests can seed jobs/milestones directly.
func newTestServiceAll(t *testing.T) (*Service, *mockrepo.CharacterRepository, *mockrepo.AchievementRepository) {
	t.Helper()
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	svc := NewService(chars, ach, mockrepo.NewCensusRunFake())
	return svc, chars, ach
}

// storeProfile stores a Lodestone profile the way a census handler does:
// PrepareCharacter, then one committed write.
func storeProfile(ctx context.Context, svc *Service, chars *mockrepo.CharacterRepository, char *contract.CharacterProfile) error {
	w, err := svc.PrepareCharacter(char)
	if err != nil {
		return err
	}
	return chars.StoreResult(ctx, contract.CensusResult{Characters: []contract.CharacterWrite{w}})
}

// processMilestones stores an achievement summary the way a census handler
// does: PrepareMilestoneResults, then one committed write.
func processMilestones(ctx context.Context, svc *Service, chars *mockrepo.CharacterRepository, charID uint32, summary *contract.AchievementSummary) ([]contract.CharacterMilestone, error) {
	w, err := svc.PrepareMilestoneResults(ctx, charID, summary)
	if err != nil {
		return nil, err
	}
	if err := mockwriter.NewStoring(chars, svc.achievements).Write(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{w}}); err != nil {
		return nil, err
	}
	return w.Milestones, nil
}

// setLatestAchievement sets a character's latest-achievement time through
// StoreResult, the one write path.
func setLatestAchievement(ctx context.Context, chars *mockrepo.CharacterRepository, id uint32, at *time.Time) error {
	return chars.StoreResult(ctx, contract.CensusResult{Achievements: []contract.AchievementWrite{{CharacterID: id, LatestAt: at}}})
}

func TestService_StoreProfile(t *testing.T) {
	svc, chars := newTestService(t)

	char := &contract.CharacterProfile{
		ID:              123,
		Name:            "Tataru Taru",
		World:           "Ultros",
		Datacenter:      "Primal",
		Gender:          2,
		Race:            "Lalafell",
		Tribe:           "Dunesfolk",
		GrandCompany:    "Maelstrom",
		FreeCompanyID:   "9234567890123456789",
		FreeCompanyName: "The Scions",
		ClassJobs: []contract.ClassJobRecord{
			{ClassJobID: 19, Name: "Paladin", Level: 90, ExpLevel: 12345},
			{ClassJobID: 25, Name: "White Mage", Level: 90, ExpLevel: 0},
		},
	}

	if err := storeProfile(context.Background(), svc, chars, char); err != nil {
		t.Fatalf("UpsertCharacter: %v", err)
	}
	got, err := chars.Get(context.Background(), 123)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %+v", err, got)
	}
	if got.Region != "NA" {
		t.Errorf("region = %q, want NA (derived from Primal)", got.Region)
	}
	if got.Race != "Lalafell" || got.GrandCompany != "Maelstrom" {
		t.Errorf("got %+v", got)
	}
	if got.FreeCompanyID == nil || *got.FreeCompanyID != "9234567890123456789" {
		t.Errorf("free company id = %v", got.FreeCompanyID)
	}
	jobs, _ := chars.GetJobs(context.Background(), 123)
	if len(jobs) != 2 {
		t.Errorf("jobs = %d, want 2", len(jobs))
	}
}

func TestService_StreamCharacters(t *testing.T) {
	svc, chars := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 1, Name: "Char 1", World: "Ultros", FirstSeenAt: now}, nil)
	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 2, Name: "Char 2", World: "Leviathan", FirstSeenAt: now}, nil)

	var streamed []uint32
	err := svc.StreamCharacters(ctx, contract.CharacterFilter{World: "Ultros"}, func(rec contract.CharacterRecord) error {
		streamed = append(streamed, rec.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamCharacters: %v", err)
	}
	if len(streamed) != 1 || streamed[0] != 1 {
		t.Fatalf("streamed = %v, want [1]", streamed)
	}
}

func TestService_StoreProfile_NilGuard(t *testing.T) {
	svc, chars := newTestService(t)
	if err := storeProfile(context.Background(), svc, chars, nil); err == nil {
		t.Fatal("expected error for nil character")
	}
}

// A profile without usable race data is hidden on Lodestone (private profile
// or suppressed race/clan "----"). It must not be persisted and must not
// erase an existing row.
func TestService_StoreProfile_HiddenProfileNotStored(t *testing.T) {
	svc, chars := newTestService(t)
	ctx := context.Background()

	for _, race := range []string{"", "----", "   "} {
		char := &contract.CharacterProfile{
			ID: 1, Name: "Hidden", World: "Ultros", Datacenter: "Primal", Race: race,
			ClassJobs: []contract.ClassJobRecord{{ClassJobID: 1, Name: "Gladiator", Level: 10}},
		}
		if err := storeProfile(ctx, svc, chars, char); !errors.Is(err, ErrProfileHidden) {
			t.Fatalf("race %q: err = %v, want ErrProfileHidden", race, err)
		}
		if got, _ := chars.Get(ctx, 1); got != nil {
			t.Fatalf("race %q: character stored, want skipped", race)
		}
		if jobs, _ := chars.GetJobs(ctx, 1); len(jobs) != 0 {
			t.Fatalf("race %q: %d jobs stored, want 0", race, len(jobs))
		}
	}

	// An existing row keeps its last known good data when a re-census hits a
	// now-hidden profile.
	if err := storeProfile(ctx, svc, chars, &contract.CharacterProfile{ID: 2, Name: "Good", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}
	if err := storeProfile(ctx, svc, chars, &contract.CharacterProfile{ID: 2, Name: "Good", World: "Ultros", Datacenter: "Primal", Race: ""}); !errors.Is(err, ErrProfileHidden) {
		t.Fatalf("re-census err = %v, want ErrProfileHidden", err)
	}
	got, _ := chars.Get(ctx, 2)
	if got == nil || got.Race != "Hyur" {
		t.Fatalf("existing row = %+v, want race Hyur preserved", got)
	}
}

func TestService_StoreProfile_Profile(t *testing.T) {
	svc, chars := newTestService(t)
	ctx := context.Background()

	char := &contract.CharacterProfile{
		ID:         555,
		Name:       "Y'shtola Rhul",
		World:      "Louisoix",
		Datacenter: "Chaos",
		Race:       "Miqo'te",
		Bio:        "Sorceress of the Night's Blessed",
		ActiveJob:  "Black Mage",
	}

	if err := storeProfile(ctx, svc, chars, char); err != nil {
		t.Fatalf("UpsertCharacter: %v", err)
	}

	got, err := chars.Get(ctx, 555)
	if err != nil || got == nil {
		t.Fatalf("Get: %v / %+v", err, got)
	}
	if got.Bio != char.Bio || got.ActiveJob != "Black Mage" {
		t.Errorf("profile fields mismatch: %+v", got)
	}
}

func TestService_MaxCharacterID(t *testing.T) {
	svc, chars := newTestService(t)

	maxID, err := svc.MaxCharacterID(context.Background())
	if err != nil {
		t.Fatalf("MaxCharacterID: %v", err)
	}
	if maxID != 0 {
		t.Fatalf("MaxCharacterID = %d, want 0", maxID)
	}

	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 100, Name: "A"}, nil)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 999, Name: "B"}, nil)

	maxID, err = svc.MaxCharacterID(context.Background())
	if err != nil {
		t.Fatalf("MaxCharacterID: %v", err)
	}
	if maxID != 999 {
		t.Fatalf("MaxCharacterID = %d, want 999", maxID)
	}
}

func TestService_ProcessMilestones(t *testing.T) {
	svc, chars := newTestService(t)
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	// The character must exist before the summary update.
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 123, Name: "X", FirstSeenAt: time.Now()}, nil)

	latest := contract.AchievementResult{AchievementID: 999, Name: "Some Other Achievement", Earned: true, EarnedAt: time.Now().Add(-1 * time.Hour)}
	summary := &contract.AchievementSummary{
		Private:           false,
		TotalAchievements: 2,
		TotalPoints:       25,
		Milestones: []contract.AchievementResult{
			{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: time.Now().Add(-48 * time.Hour)},
			{AchievementID: 999, Name: "Some Other Achievement", Earned: true, EarnedAt: latest.EarnedAt},
		},
		LatestAchievement: &latest,
	}

	ms, err := processMilestones(context.Background(), svc, chars, 123, summary)
	if err != nil {
		t.Fatalf("ProcessAchievements: %v", err)
	}
	// Only the registered milestone (590) is kept; 999 is filtered out.
	if len(ms) != 1 || ms[0].AchievementID != 590 {
		t.Errorf("milestones = %+v, want only 590", ms)
	}
	// Latest achievement (any, not just milestone) is 999 at -1h.
	got, _ := chars.Get(context.Background(), 123)
	if got.LatestAchievementID == nil || *got.LatestAchievementID != 999 {
		t.Errorf("latest achievement = %v, want 999", got.LatestAchievementID)
	}
	if got.AchievementsPrivate {
		t.Error("achievements_private = true, want false")
	}
}

func TestService_ProcessMilestones_Private(t *testing.T) {
	svc, chars := newTestService(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 5, Name: "X", FirstSeenAt: time.Now()}, nil)

	summary := &contract.AchievementSummary{Private: true}
	if _, err := processMilestones(context.Background(), svc, chars, 5, summary); err != nil {
		t.Fatalf("ProcessAchievements: %v", err)
	}
	got, _ := chars.Get(context.Background(), 5)
	if !got.AchievementsPrivate {
		t.Error("achievements_private = false, want true")
	}
}

func TestService_PrepareMilestoneResults(t *testing.T) {
	svc, chars := newTestService(t)
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	earned := time.Now().Add(-48 * time.Hour)
	latest := contract.AchievementResult{AchievementID: 999, Earned: true, EarnedAt: time.Now().Add(-time.Hour)}
	summary := &contract.AchievementSummary{
		Milestones: []contract.AchievementResult{
			{AchievementID: 590, Earned: true, EarnedAt: earned},
			{AchievementID: 591, Earned: false},
			{AchievementID: 999, Earned: true, EarnedAt: latest.EarnedAt},
		},
		LatestAchievement: &latest,
	}

	w, err := svc.PrepareMilestoneResults(context.Background(), 123, summary)
	if err != nil {
		t.Fatalf("PrepareMilestoneResults: %v", err)
	}

	if w.CharacterID != 123 || w.Private {
		t.Fatalf("write = %+v", w)
	}
	if len(w.Milestones) != 1 || w.Milestones[0].AchievementID != 590 || w.Milestones[0].CharacterID != 123 || !w.Milestones[0].AchievedAt.Equal(earned) {
		t.Fatalf("milestones = %+v, want only earned registered 590", w.Milestones)
	}
	if w.LatestID == nil || *w.LatestID != 999 || w.LatestAt == nil || !w.LatestAt.Equal(latest.EarnedAt) {
		t.Fatalf("latest = %v @ %v, want 999", w.LatestID, w.LatestAt)
	}
	if chars.UpsertCalls != 0 || len(chars.StoredAchievements) != 0 {
		t.Fatal("PrepareMilestoneResults must not store anything")
	}
}

func TestService_PrepareMilestoneResults_PrivateAndErrors(t *testing.T) {
	svc, _ := newTestService(t)
	w, err := svc.PrepareMilestoneResults(context.Background(), 5, &contract.AchievementSummary{Private: true})
	if err != nil || !w.Private || w.CharacterID != 5 || len(w.Milestones) != 0 {
		t.Fatalf("private write = %+v, %v", w, err)
	}
	if _, err := svc.PrepareMilestoneResults(context.Background(), 5, nil); err == nil {
		t.Fatal("nil summary: want error")
	}
	if _, err := svc.PrepareMilestoneResults(context.Background(), 5, &contract.AchievementSummary{}); err == nil {
		t.Fatal("empty registry: want error")
	}
}

func TestService_IsActive(t *testing.T) {
	svc, _ := newTestService(t)
	now := time.Now().UTC()
	if !svc.IsActive(now.Add(-time.Hour)) {
		t.Error("achievement 1h ago should be active within the default 90d window")
	}
	if !svc.IsActive(now.Add(-60 * 24 * time.Hour)) {
		t.Error("achievement 60d ago should be active within the default 90d window")
	}
	if svc.IsActive(now.Add(-91 * 24 * time.Hour)) {
		t.Error("achievement 91d ago should not be active")
	}
}

func TestService_IsActive_ZeroTime(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.IsActive(time.Time{}) {
		t.Error("zero time should not be active")
	}
}

func TestService_ProcessMilestones_PreservesMilestonesOnPrivate(t *testing.T) {
	svc, chars := newTestService(t)
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 7, Name: "X", FirstSeenAt: time.Now()}, nil)

	// First, process public achievements so a milestone + latest are recorded.
	now590 := time.Now()
	publicSummary := &contract.AchievementSummary{
		Milestones: []contract.AchievementResult{
			{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: now590},
		},
		LatestAchievement: &contract.AchievementResult{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: now590},
	}
	if _, err := processMilestones(context.Background(), svc, chars, 7, publicSummary); err != nil {
		t.Fatalf("ProcessMilestoneResults (public): %v", err)
	}

	// Now the profile goes private: milestones and latest must be preserved.
	if _, err := processMilestones(context.Background(), svc, chars, 7, &contract.AchievementSummary{Private: true}); err != nil {
		t.Fatalf("ProcessMilestoneResults (private): %v", err)
	}
	got, _ := chars.Get(context.Background(), 7)
	if !got.AchievementsPrivate {
		t.Error("achievements_private = false, want true")
	}
	if got.LatestAchievementID == nil || *got.LatestAchievementID != 590 {
		t.Errorf("latest achievement was wiped on private turn: %v", got.LatestAchievementID)
	}
	milestones, err := svc.achievements.ListCharacterMilestones(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListCharacterMilestones: %v", err)
	}
	if len(milestones) != 1 || milestones[0].AchievementID != 590 {
		t.Errorf("milestones = %+v, want only 590 preserved", milestones)
	}
}

func TestService_ProcessMilestones_EmptyRegistry(t *testing.T) {
	svc, chars := newTestService(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 9, Name: "X", FirstSeenAt: time.Now()}, nil)

	// No SyncMilestones: registry is empty, processing must error rather than wipe.
	if _, err := processMilestones(context.Background(), svc, chars, 9, &contract.AchievementSummary{}); err == nil {
		t.Fatal("expected error for empty milestone registry")
	}
}

func TestService_Summary(t *testing.T) {
	svc, chars := newTestService(t)
	svc.SetActivityWindow(30 * 24 * time.Hour)
	ctx := context.Background()
	now := time.Now().UTC()
	active := now.Add(-time.Hour)
	inactive := now.Add(-60 * 24 * time.Hour)

	seed := func(id uint32, lat *time.Time, lvl uint8) {
		var jobs []contract.ClassJobRecord
		if lvl > 0 {
			jobs = []contract.ClassJobRecord{
				{CharacterID: id, Name: "Paladin", Level: lvl},
			}
		}
		_ = chars.Upsert(ctx, contract.CharacterRecord{ID: id, Name: fmt.Sprintf("c%d", id), World: "Ultros", FirstSeenAt: now}, jobs)
		if lat != nil {
			_ = setLatestAchievement(ctx, chars, id, lat)
		}
	}
	seed(1, &active, 100)  // active within window, level 100
	seed(2, &inactive, 90) // outside window, level 90
	seed(3, nil, 0)        // never achievement-censused, no jobs
	seed(4, &active, 100)  // active and level 100, but deleted below
	_ = chars.MarkDeleted(ctx, 4, now)

	total, gotActive, maxLvl, err := svc.Summary(ctx)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (deleted excluded)", total)
	}
	if gotActive != 1 {
		t.Errorf("active = %d, want 1", gotActive)
	}
	if maxLvl != 1 {
		t.Errorf("maxLvl = %d, want 1", maxLvl)
	}
}

func TestService_Config(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.MaxLevel() != 100 {
		t.Errorf("default MaxLevel = %d, want 100", svc.MaxLevel())
	}
	if len(svc.Expansions()) != 6 {
		t.Errorf("default len(Expansions) = %d, want 6", len(svc.Expansions()))
	}
	customExp := []ExpansionConfig{
		{Name: "Custom Exp", Version: "Patch 8.0", FinalQuest: "The End", Icon: "🌟", LevelCap: 110, AchievementID: 9999},
	}
	svc.SetConfig(110, customExp)
	if svc.MaxLevel() != 110 {
		t.Errorf("custom MaxLevel = %d, want 110", svc.MaxLevel())
	}
	if len(svc.Expansions()) != 1 || svc.Expansions()[0].Name != "Custom Exp" {
		t.Errorf("custom Expansions = %+v", svc.Expansions())
	}
	milestones := svc.Milestones()
	if len(milestones) != 2 {
		t.Fatalf("len(Milestones) = %d, want 2 (chocobo + custom)", len(milestones))
	}
	if milestones[0].AchievementID != 590 || milestones[1].AchievementID != 9999 {
		t.Errorf("milestones = %+v", milestones)
	}
}

func TestService_ListCharacters_Pagination(t *testing.T) {
	svc, chars := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []uint32{1, 2, 3} {
		_ = chars.Upsert(ctx, contract.CharacterRecord{ID: id, Name: fmt.Sprintf("c%d", id), World: "Ultros", FirstSeenAt: now}, nil)
	}
	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 4, Name: "gone", World: "Ultros", FirstSeenAt: now}, nil)
	_ = chars.MarkDeleted(ctx, 4, now)

	page, total, err := svc.ListCharacters(ctx, contract.CharacterFilter{}, 2, 0)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (deleted excluded)", total)
	}
	if len(page) != 2 || page[0].ID != 1 || page[1].ID != 2 {
		t.Errorf("page(2,0) = %+v, want ids [1 2]", page)
	}

	page, total, err = svc.ListCharacters(ctx, contract.CharacterFilter{}, 2, 2)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page) != 1 || page[0].ID != 3 {
		t.Errorf("page(2,2) = %+v, want id [3]", page)
	}
}

func TestService_ListCharacters_Filter(t *testing.T) {
	svc, chars := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 1, Name: "Feed How", World: "Louisoix", Race: "Au Ra", FirstSeenAt: now}, nil)
	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 2, Name: "Ninto Thegen", World: "Louisoix", Race: "Miqo'te", FirstSeenAt: now}, nil)
	_ = chars.Upsert(ctx, contract.CharacterRecord{ID: 3, Name: "Ahribella White", World: "Zodiark", Race: "Miqo'te", FirstSeenAt: now}, nil)

	page, total, err := svc.ListCharacters(ctx, contract.CharacterFilter{World: "Louisoix"}, 10, 0)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if total != 2 || len(page) != 2 {
		t.Fatalf("total = %d, len = %d, want 2", total, len(page))
	}
	if page[0].ID != 1 || page[1].ID != 2 {
		t.Errorf("got ids [%d, %d], want [1, 2]", page[0].ID, page[1].ID)
	}
}

func TestService_CharacterDetail_Missing(t *testing.T) {
	svc, _ := newTestService(t)
	got, err := svc.CharacterDetail(context.Background(), 999)
	if err != nil {
		t.Fatalf("CharacterDetail: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil for unknown id", got)
	}
}

func TestService_CharacterDetail_WithJobsAndMilestones(t *testing.T) {
	svc, chars, ach := newTestServiceAll(t)
	ctx := context.Background()
	now := time.Now().UTC()
	fcID := "9234567890123456789"
	fcName := "The Scions"
	_ = chars.Upsert(ctx, contract.CharacterRecord{
		ID: 123, Name: "Tataru Taru", World: "Ultros", Datacenter: "Primal", Region: "NA",
		Race: "Lalafell", Gender: 2, FirstSeenAt: now,
		FreeCompanyID:       &fcID,
		FreeCompanyName:     &fcName,
		LatestAchievementAt: &now,
	}, []contract.ClassJobRecord{
		{CharacterID: 123, ClassJobID: 19, Name: "Paladin", Level: 90, ExpLevel: 12345},
	})
	_ = ach.UpsertCharacterMilestones(ctx, 123, []contract.CharacterMilestone{
		{CharacterID: 123, AchievementID: 590, AchievedAt: now},
	})

	got, err := svc.CharacterDetail(ctx, 123)
	if err != nil {
		t.Fatalf("CharacterDetail: %v", err)
	}
	if got == nil {
		t.Fatal("CharacterDetail = nil, want detail")
	}
	if got.Character.ID != 123 || got.Character.Name != "Tataru Taru" {
		t.Errorf("Character = %+v", got.Character)
	}
	if len(got.Jobs) != 1 || got.Jobs[0].ClassJobID != 19 || got.Jobs[0].Level != 90 {
		t.Errorf("Jobs = %+v", got.Jobs)
	}
	if len(got.Milestones) != 1 || got.Milestones[0].AchievementID != 590 {
		t.Errorf("Milestones = %+v", got.Milestones)
	}
}

func TestService_Breakdown_InvalidDimension(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Breakdown(context.Background(), "level")
	if !errors.Is(err, ErrInvalidDimension) {
		t.Fatalf("err = %v, want ErrInvalidDimension", err)
	}
}

func TestService_Breakdown_Delegates(t *testing.T) {
	svc, chars := newTestService(t)
	svc.SetActivityWindow(30 * 24 * time.Hour)
	ctx := context.Background()
	now := time.Now().UTC()
	active := now.Add(-time.Hour)
	inactive := now.Add(-60 * 24 * time.Hour)

	seed := func(id uint32, world string, lat *time.Time) {
		_ = chars.Upsert(ctx, contract.CharacterRecord{ID: id, Name: fmt.Sprintf("c%d", id), World: world, FirstSeenAt: now}, nil)
		if lat != nil {
			_ = setLatestAchievement(ctx, chars, id, lat)
		}
	}
	seed(1, "Ultros", &active)
	seed(2, "Ultros", &inactive)
	seed(3, "Leviathan", &active)

	got, err := svc.Breakdown(ctx, "world")
	if err != nil {
		t.Fatalf("Breakdown: %v", err)
	}
	want := []contract.GroupCount{
		{Key: "Ultros", Total: 2, Active: 1},
		{Key: "Leviathan", Total: 1, Active: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Breakdown = %+v, want %+v", got, want)
	}
}

func TestService_WorldDetail(t *testing.T) {
	svc, chars, ach := newTestServiceAll(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = chars.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "Char 1", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Hyur", FirstSeenAt: now,
		LatestAchievementAt: &now,
	}, nil)
	_ = chars.Upsert(ctx, contract.CharacterRecord{
		ID: 2, Name: "Char 2", World: "Balmung", Datacenter: "Crystal", Region: "NA", Race: "Elezen", FirstSeenAt: now,
	}, nil)
	_ = chars.Upsert(ctx, contract.CharacterRecord{
		ID: 3, Name: "Char 3", World: "Mateus", Datacenter: "Crystal", Region: "NA", Race: "Hyur", FirstSeenAt: now,
	}, nil)

	ach.ChocoboCountResponse = 2
	ach.ExpansionsResponse = []contract.ExpansionCount{{Expansion: "Endwalker", Count: 1}}
	ach.NewCharactersResponse = []contract.DailyCount{{Day: "2026-08-17", Count: 2}}

	detail, err := svc.WorldDetail(ctx, "Balmung")
	if err != nil {
		t.Fatalf("WorldDetail: %v", err)
	}
	if detail.World != "Balmung" {
		t.Errorf("World = %q, want Balmung", detail.World)
	}
	if detail.TotalCharacters != 2 {
		t.Errorf("TotalCharacters = %d, want 2", detail.TotalCharacters)
	}
	if detail.ActiveCharacters != 1 {
		t.Errorf("ActiveCharacters = %d, want 1", detail.ActiveCharacters)
	}
	if detail.NewCharacters30d != 2 {
		t.Errorf("NewCharacters30d = %d, want 2", detail.NewCharacters30d)
	}
	if len(detail.Races) != 2 {
		t.Errorf("Races len = %d, want 2", len(detail.Races))
	}
	if len(detail.MSQCompletions) != 1 {
		t.Errorf("MSQCompletions len = %d, want 1", len(detail.MSQCompletions))
	}
	if len(detail.NewCharactersTimeline) != 1 {
		t.Errorf("NewCharactersTimeline len = %d, want 1", len(detail.NewCharactersTimeline))
	}
}

func TestService_NewCharacters(t *testing.T) {
	svc, _, ach := newTestServiceAll(t)
	ctx := context.Background()
	mk := func(day int) time.Time { return time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC) }

	ach.NewCharactersResponse = []contract.DailyCount{
		{Day: "2026-08-11", Count: 2},
		{Day: "2026-08-13", Count: 1},
	}

	got, err := svc.NewCharacters(ctx, mk(10), mk(14))
	if err != nil {
		t.Fatalf("NewCharacters: %v", err)
	}
	want := []contract.DailyCount{
		{Day: "2026-08-11", Count: 2},
		{Day: "2026-08-13", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewCharacters = %+v, want %+v", got, want)
	}
}

func TestService_ExpansionCompletions(t *testing.T) {
	svc, _, ach := newTestServiceAll(t)
	ctx := context.Background()
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	now := time.Now().UTC()
	_ = ach.UpsertCharacterMilestones(ctx, 1, []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 1139, AchievedAt: now}, // Heavensward
		{CharacterID: 1, AchievementID: 1794, AchievedAt: now}, // Stormblood
	})
	_ = ach.UpsertCharacterMilestones(ctx, 2, []contract.CharacterMilestone{
		{CharacterID: 2, AchievementID: 1139, AchievedAt: now},
	})
	_ = ach.UpsertCharacterMilestones(ctx, 3, []contract.CharacterMilestone{
		{CharacterID: 3, AchievementID: 590, AchievedAt: now}, // non-expansion milestone
	})

	got, err := svc.ExpansionCompletions(ctx)
	if err != nil {
		t.Fatalf("ExpansionCompletions: %v", err)
	}
	want := []contract.ExpansionCount{
		{Expansion: "Heavensward", Count: 2},
		{Expansion: "Stormblood", Count: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExpansionCompletions = %+v, want %+v", got, want)
	}
}

func TestService_SetActivityWindow_Noop(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetActivityWindow(0)
	svc.SetActivityWindow(-time.Hour)
	if !svc.IsActive(time.Now().UTC().Add(-15 * 24 * time.Hour)) {
		t.Error("no-op SetActivityWindow should keep the default 30-day window")
	}
}

// barrier is a synchronisation primitive for proving concurrent execution.
// Each goroutine calls enter(), which blocks until the expected number of
// goroutines have arrived. Then all are released.
type barrier struct {
	mu      sync.Mutex
	entered int
	expect  int
	ready   chan struct{}
	release chan struct{}
}

func newBarrier(expect int) *barrier {
	return &barrier{
		expect:  expect,
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

// enter blocks until all expected goroutines have entered.
func (b *barrier) enter() {
	b.mu.Lock()
	b.entered++
	if b.entered >= b.expect {
		close(b.ready)
	}
	b.mu.Unlock()
	<-b.release
}

// release waits for all goroutines to enter, then unblocks them.
func (b *barrier) releaseAll() {
	<-b.ready
	close(b.release)
}

// barrierCharRepo wraps CharacterRepository with barrier-gated methods.
type barrierCharRepo struct {
	*mockrepo.CharacterRepository
	countBarrier       *barrier
	countActiveBarrier *barrier
}

func (b *barrierCharRepo) Count(ctx context.Context, filter contract.CharacterFilter) (int64, error) {
	b.countBarrier.enter()
	return b.CharacterRepository.Count(ctx, filter)
}

func (b *barrierCharRepo) CountActive(ctx context.Context, since time.Time) (int64, error) {
	b.countActiveBarrier.enter()
	return b.CharacterRepository.CountActive(ctx, since)
}

// barrierAchRepo wraps AchievementRepository with barrier-gated methods.
type barrierAchRepo struct {
	*mockrepo.AchievementRepository
	countExpBarrier *barrier
}

func (b *barrierAchRepo) CountExpansions(ctx context.Context) ([]contract.ExpansionCount, error) {
	b.countExpBarrier.enter()
	return b.AchievementRepository.CountExpansions(ctx)
}

func TestService_SummaryQueriesRunConcurrently(t *testing.T) {
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	runs := mockrepo.NewCensusRunFake()

	// All 3 Summary queries must enter before any completes.
	b := newBarrier(3)
	wrapped := &barrierCharRepo{
		CharacterRepository: chars,
		countBarrier:        b,
		countActiveBarrier:  b,
	}
	svc := NewService(wrapped, ach, runs)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = svc.Summary(context.Background())
	}()

	// Wait for all 3 to enter, then release them.
	b.releaseAll()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Summary did not complete: queries are serial, not concurrent")
	}
}

// worldDetailBarrierCharRepo gates Count calls behind a barrier to prove
// WorldDetail's two Count calls run concurrently.
type worldDetailBarrierCharRepo struct {
	*mockrepo.CharacterRepository
	countBarrier *barrier
}

func (w *worldDetailBarrierCharRepo) Count(ctx context.Context, filter contract.CharacterFilter) (int64, error) {
	w.countBarrier.enter()
	return w.CharacterRepository.Count(ctx, filter)
}

func TestService_WorldDetailQueriesRunConcurrently(t *testing.T) {
	chars := mockrepo.NewCharacterFake()
	ach := mockrepo.NewAchievementFake()
	runs := mockrepo.NewCensusRunFake()

	// WorldDetail calls Count twice (total and active). If serial, the second
	// blocks on the first behind the barrier, and the test times out.
	b := newBarrier(2)
	wrapped := &worldDetailBarrierCharRepo{
		CharacterRepository: chars,
		countBarrier:        b,
	}
	svc := NewService(wrapped, ach, runs)

	ctx := context.Background()
	_ = chars.Upsert(ctx, contract.CharacterRecord{
		ID: 1, Name: "Test", World: "Balmung", Datacenter: "Crystal", Region: "NA",
		FirstSeenAt: time.Now().UTC(),
	}, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.WorldDetail(ctx, "Balmung")
	}()

	b.releaseAll()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WorldDetail did not complete: Count calls are serial, not concurrent")
	}
}

func TestService_IsActive_ConfiguredWindow(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetActivityWindow(7 * 24 * time.Hour)
	now := time.Now().UTC()
	if !svc.IsActive(now.Add(-6 * 24 * time.Hour)) {
		t.Error("achievement 6d ago should be active within a 7d window")
	}
	if svc.IsActive(now.Add(-8 * 24 * time.Hour)) {
		t.Error("achievement 8d ago should not be active within a 7d window")
	}
}

func TestService_CachedMilestones_ReturnsFreshData(t *testing.T) {
	svc, _, ach := newTestServiceAll(t)
	ctx := context.Background()
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}

	got, err := svc.cachedMilestones(ctx)
	if err != nil {
		t.Fatalf("cachedMilestones: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("cachedMilestones returned empty slice")
	}
	// Verify it matches what the DB has.
	dbGot, err := ach.ListMilestones(ctx)
	if err != nil {
		t.Fatalf("ListMilestones: %v", err)
	}
	if len(got) != len(dbGot) {
		t.Errorf("cachedMilestones len = %d, want %d", len(got), len(dbGot))
	}
}

func TestService_CachedMilestones_UsesCache(t *testing.T) {
	svc, _, ach := newTestServiceAll(t)
	ctx := context.Background()
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}

	// First call populates the cache.
	if _, err := svc.cachedMilestones(ctx); err != nil {
		t.Fatalf("cachedMilestones (first): %v", err)
	}

	// Mutate the underlying fake to prove the cache is serving stale data.
	ach.SyncErr = errors.New("should not be called")

	// Second call within TTL should return cached data without hitting the DB.
	got, err := svc.cachedMilestones(ctx)
	if err != nil {
		t.Fatalf("cachedMilestones (second): %v", err)
	}
	if len(got) == 0 {
		t.Fatal("cachedMilestones returned empty on cache hit")
	}
}

func TestService_SyncMilestones_InvalidatesCache(t *testing.T) {
	svc, _, _ := newTestServiceAll(t)
	ctx := context.Background()
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}

	// Populate the cache.
	if _, err := svc.cachedMilestones(ctx); err != nil {
		t.Fatalf("cachedMilestones: %v", err)
	}

	// SyncMilestones should invalidate the cache.
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones (second): %v", err)
	}

	// The next cachedMilestones call should query the DB again.
	// Verify by checking that the cache was reset (cache time is zero).
	svc.mu.RLock()
	cacheAt := svc.milestoneCacheAt
	svc.mu.RUnlock()
	if !cacheAt.IsZero() {
		t.Error("milestoneCacheAt should be zero after SyncMilestones invalidation")
	}
}

func TestService_MilestoneIDs(t *testing.T) {
	svc, _, _ := newTestServiceAll(t)
	ctx := context.Background()
	if err := svc.SyncMilestones(ctx); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}

	ids, err := svc.MilestoneIDs(ctx)
	if err != nil {
		t.Fatalf("MilestoneIDs: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("MilestoneIDs returned empty set")
	}
	// The default expansions should include milestone 590 (Chocobo).
	if !ids[590] {
		t.Error("MilestoneIDs should contain 590 (Chocobo milestone)")
	}
}

func TestService_ListCharacterMilestones(t *testing.T) {
	svc, _, ach := newTestServiceAll(t)
	ctx := context.Background()

	// Seed milestones for character 1.
	now := time.Now()
	milestones := []contract.CharacterMilestone{
		{CharacterID: 1, AchievementID: 590, AchievedAt: now.Add(-time.Hour)},
		{CharacterID: 1, AchievementID: 1129, AchievedAt: now},
	}
	if err := ach.UpsertCharacterMilestones(ctx, 1, milestones); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := svc.ListCharacterMilestones(ctx, 1)
	if err != nil {
		t.Fatalf("ListCharacterMilestones: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d milestones, want 2", len(got))
	}
	// Verify IDs present.
	ids := map[uint32]bool{}
	for _, m := range got {
		ids[m.AchievementID] = true
	}
	if !ids[590] || !ids[1129] {
		t.Errorf("unexpected milestones: %+v", got)
	}
}

func TestService_GetCharacter(t *testing.T) {
	svc, chars, _ := newTestServiceAll(t)
	ctx := context.Background()

	now := time.Now()
	if err := chars.Upsert(ctx, contract.CharacterRecord{ID: 42, Name: "Test", FirstSeenAt: now}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := svc.GetCharacter(ctx, 42)
	if err != nil {
		t.Fatalf("GetCharacter: %v", err)
	}
	if got == nil || got.ID != 42 {
		t.Errorf("got %+v, want ID 42", got)
	}

	// Non-existent character returns nil, nil.
	missing, err := svc.GetCharacter(ctx, 99999)
	if err != nil {
		t.Fatalf("GetCharacter missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for missing character, got %+v", missing)
	}
}

func TestService_ActivityWindowDefaultsTo90Days(t *testing.T) {
	svc := NewService(nil, nil, nil)
	if got := svc.ActivityWindow(); got != 90*24*time.Hour {
		t.Fatalf("ActivityWindow() = %v, want 90 days", got)
	}
	svc.SetActivityWindow(45 * 24 * time.Hour)
	if got := svc.ActivityWindow(); got != 45*24*time.Hour {
		t.Fatalf("ActivityWindow() after override = %v, want 45 days", got)
	}
}
