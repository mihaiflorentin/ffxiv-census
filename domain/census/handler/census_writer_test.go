package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestAchievementCensus_WithWriterHandsMilestonesAndLatestToWriter(t *testing.T) {
	h, ls, chars, ach := newTestAchievementCensus(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	now := time.Now()
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{
			Milestones: []contract.AchievementResult{
				{AchievementID: 590, Earned: true, EarnedAt: now.Add(-time.Hour)},
				{AchievementID: 999, Earned: true, EarnedAt: now},
			},
			LatestAchievement: &contract.AchievementResult{AchievementID: 999, Earned: true, EarnedAt: now},
		}, nil
	}

	next, err := h.Handle(context.Background(), achievementPayload(123))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next jobs = %d, want 0 (leaf event)", len(next))
	}

	results := w.Results()
	if len(results) != 1 || len(results[0].Achievements) != 1 || len(results[0].Characters) != 0 {
		t.Fatalf("writer results = %+v, want one achievement write", results)
	}
	got := results[0].Achievements[0]
	if got.CharacterID != 123 || len(got.Milestones) != 1 || got.Milestones[0].AchievementID != 590 {
		t.Fatalf("achievement write = %+v, want only milestone 590", got)
	}
	if got.LatestID == nil || *got.LatestID != 999 {
		t.Fatalf("latest = %v, want 999", got.LatestID)
	}
	if len(chars.StoredAchievements) != 0 {
		t.Fatal("repository written directly; with a writer all storage goes through it")
	}
	if ms, _ := ach.ListCharacterMilestones(context.Background(), 123); len(ms) != 0 {
		t.Fatalf("milestones stored directly: %+v", ms)
	}
}

func TestAchievementCensus_WithWriterPrivateProfileWritesPrivateFlag(t *testing.T) {
	h, ls, _, _ := newTestAchievementCensus(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{Private: true}, nil
	}

	if _, err := h.Handle(context.Background(), achievementPayload(5)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	r := w.Results()
	if len(r) != 1 || len(r[0].Achievements) != 1 || !r[0].Achievements[0].Private {
		t.Fatalf("results = %+v, want one private achievement write", r)
	}
}

func TestAchievementCensus_WithWriterFailureFailsTheJob(t *testing.T) {
	h, ls, _, _ := newTestAchievementCensus(t)
	w := mockwriter.NewFake()
	boom := errors.New("commit failed")
	w.WriteErr = boom
	h = h.WithWriter(w)
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{}, nil
	}

	if _, err := h.Handle(context.Background(), achievementPayload(5)); !errors.Is(err, boom) {
		t.Fatalf("Handle error = %v, want %v", err, boom)
	}
}

func TestAchievementCensus_WithWriterStillMarksDeletedDirectly(t *testing.T) {
	h, ls, chars, _ := newTestAchievementCensus(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 8, Name: "X", FirstSeenAt: time.Now()}, nil)
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return nil, contract.ErrCharacterNotFound
	}

	if _, err := h.Handle(context.Background(), achievementPayload(8)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got, _ := chars.Get(context.Background(), 8); got == nil || got.DeletedAt == nil {
		t.Fatalf("character not marked deleted: %+v", got)
	}
	if len(w.Results()) != 0 {
		t.Fatal("writer received a result for a deleted character")
	}
}

func TestCharacterCensus_WithWriterHandsCharacterAndJobsToWriter(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(0, 0)

	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := w.Characters()
	if len(got) != 1 || got[0].Record.ID != 42 || len(got[0].Jobs) != 1 || got[0].Jobs[0].ClassJobID != 19 {
		t.Fatalf("writer characters = %+v, want character 42 with its Paladin job", got)
	}
	if len(next) == 0 {
		t.Fatal("no dependent jobs chained after a stored character")
	}
	if chars.UpsertCalls != 0 {
		t.Fatalf("repository written directly %d times; with a writer all storage goes through it", chars.UpsertCalls)
	}
}

func TestCharacterCensus_WithWriterHiddenProfileWritesNothing(t *testing.T) {
	h, ls, _ := newTestCharacterCensus(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(0, 42)

	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(w.Results()) != 0 || len(next) != 0 {
		t.Fatalf("hidden profile: results=%d next=%d, want none", len(w.Results()), len(next))
	}
}

func TestCharacterCensus_WithWriterFailureFailsTheJobAndChainsNothing(t *testing.T) {
	h, ls, _ := newTestCharacterCensus(t)
	w := mockwriter.NewFake()
	boom := errors.New("commit failed")
	w.WriteErr = boom
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(0, 0)

	next, err := h.Handle(context.Background(), characterPayload(42))
	if !errors.Is(err, boom) {
		t.Fatalf("Handle error = %v, want %v", err, boom)
	}
	if len(next) != 0 {
		t.Fatalf("chained %d jobs for a character that was never stored", len(next))
	}
}
