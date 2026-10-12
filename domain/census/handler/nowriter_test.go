package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// A handler never stores a character itself: without a result writer the job
// fails (and is retried) instead of silently writing outside the batch path.
func TestHandlers_WithoutWriterFailTheJob(t *testing.T) {
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "A", World: "Odin", Datacenter: "Light", Race: "Hyur"}, nil
	}
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	cases := []struct {
		name    string
		handler Handler
		payload []byte
	}{
		{"id-sweep", NewIDSweep(ls, svc, nil), []byte(`{"from":1,"to":1}`)},
		{"character-census", NewCharacterCensus(ls, svc, nil), characterPayload(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.handler.Handle(context.Background(), tc.payload); !errors.Is(err, ErrNoWriter) {
				t.Fatalf("Handle error = %v, want ErrNoWriter", err)
			}
			if rec, _ := chars.Get(context.Background(), 1); rec != nil {
				t.Fatal("character stored without a writer")
			}
		})
	}
}

func TestAchievementCensus_WithoutWriterFailsTheJob(t *testing.T) {
	h, ls, _, _ := newTestAchievementCensus(t)
	h.writer = nil
	ls.FetchAchievementsFunc = func(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{}, nil
	}
	if _, err := h.Handle(context.Background(), achievementPayload(1)); !errors.Is(err, ErrNoWriter) {
		t.Fatalf("Handle error = %v, want ErrNoWriter", err)
	}
}
