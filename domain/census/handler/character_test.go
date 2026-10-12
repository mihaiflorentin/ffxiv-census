package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func newTestCharacterCensus(t *testing.T) (*CharacterCensus, *mocklodestone.Fake, *mockrepo.CharacterRepository) {
	t.Helper()
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	return NewCharacterCensus(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, nil)), ls, chars
}

func characterPayload(id uint32) []byte {
	b, _ := json.Marshal(CharacterCensusPayload{CharacterID: id})
	return b
}

func TestCharacterCensus_UpsertAndChain(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur", FreeCompanyID: "9234567890123456789"}, nil
	}
	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("next jobs = %d, want 1 (achievement)", len(next))
	}
	if next[0].Type != EventAchievementCensus {
		t.Errorf("next types = %q, want %q", next[0].Type, EventAchievementCensus)
	}
	if got, _ := chars.Get(context.Background(), 42); got == nil {
		t.Errorf("character 42 should be upserted")
	}
}

func TestCharacterCensus_NoFCChainsOnlyAchievement(t *testing.T) {
	h, ls, _ := newTestCharacterCensus(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}
	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 || next[0].Type != EventAchievementCensus {
		t.Errorf("next = %+v, want only achievement-census", next)
	}
}

func TestCharacterCensus_NotFoundMarksDeleted(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	_ = chars.Upsert(context.Background(), contract.CharacterRecord{ID: 42, Name: "X", FirstSeenAt: time.Now()}, nil)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, contract.ErrCharacterNotFound
	}
	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Errorf("next jobs = %d, want 0 (deleted)", len(next))
	}
	got, _ := chars.Get(context.Background(), 42)
	if got.DeletedAt == nil {
		t.Errorf("character 42 should be marked deleted")
	}
}

func TestCharacterCensus_ReturnsDownstreamJobsInNext(t *testing.T) {
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())

	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID:            id,
			Name:          "Immediate Character",
			World:         "Ultros",
			Datacenter:    "Primal",
			Race:          "Hyur",
			FreeCompanyID: "9234567890123456789",
		}, nil
	}

	h := NewCharacterCensus(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, nil))

	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("expected 1 returned job (ach), got %d", len(next))
	}
	if next[0].Type != EventAchievementCensus {
		t.Errorf("expected first job to be achievement-census, got %q", next[0].Type)
	}
}

func TestCharacterCensus_FetchError(t *testing.T) {
	h, ls, _ := newTestCharacterCensus(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, errors.New("boom")
	}
	if _, err := h.Handle(context.Background(), characterPayload(1)); err == nil {
		t.Fatal("expected error on fetch failure")
	}
}

// A transient Lodestone error must keep its typed chain: the worker
// classifies the ProxyCheckError (destination cooldown vs proxy rotation)
// via errors.As.
func TestCharacterCensus_TransientErrorUnwrapsToProxyCheckError(t *testing.T) {
	h, ls, _ := newTestCharacterCensus(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, fmt.Errorf("character-census lodestone fetch: %w", &contract.ProxyCheckError{
			Kind: contract.CheckTarget, Reason: "status 202", Challenge: true, Err: errors.New("HTTP 202"),
		})
	}

	_, err := h.Handle(context.Background(), characterPayload(880))
	if err == nil {
		t.Fatal("expected the transient error, got nil")
	}
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || !checkErr.Challenge {
		t.Fatalf("handler error must unwrap to the typed challenge error so the worker can cool the destination, got %v", err)
	}
}

// The Lodestone is the sole authority: a genuine 404 marks the character
// deleted immediately (no second provider confirmation).
func TestCharacterCensus_NeverUpsertsHiddenProfile(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: 700, Name: "Hidden", World: "Odin", Datacenter: "Chaos", Race: ""}, nil
	}

	next, err := h.Handle(context.Background(), characterPayload(700))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Errorf("next jobs = %+v, want none for hidden profile", next)
	}
	if got, _ := chars.Get(context.Background(), 700); got != nil {
		t.Errorf("hidden-profile character was stored: %+v", got)
	}
}

func TestCharacterCensus_DoesNotChainAchievementCensusForPrivateCharacterCheckedWithinWindow(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	seedPrivate(t, chars, 42, time.Now().Add(-24*time.Hour))
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}
	next, err := h.Handle(context.Background(), characterPayload(42))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Fatalf("next = %+v, want no jobs: the achievement census would only be skipped", next)
	}
	got, _ := chars.Get(context.Background(), 42)
	if got == nil || got.LastCensusAt == nil || time.Since(*got.LastCensusAt) > time.Minute {
		t.Fatalf("the character itself must still be refreshed, got %+v", got)
	}
	if got.AchievementsCheckedAt == nil || time.Since(*got.AchievementsCheckedAt) < 12*time.Hour {
		t.Fatalf("a character refresh must not touch achievements_checked_at, got %v", got.AchievementsCheckedAt)
	}
}

func TestCharacterCensus_ChainsAchievementCensusForPrivateCharacterAfterWindow(t *testing.T) {
	h, ls, chars := newTestCharacterCensus(t)
	seedPrivate(t, chars, 43, time.Now().Add(-31*24*time.Hour))
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}
	next, err := h.Handle(context.Background(), characterPayload(43))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 || next[0].Type != EventAchievementCensus {
		t.Fatalf("next = %+v, want the achievement-census job", next)
	}
}
