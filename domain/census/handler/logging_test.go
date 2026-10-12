package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// newBufLogger returns a TextHandler logger writing to a buffer at Debug level.
func newBufLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCharacterCensus_LogsFetchAndStore(t *testing.T) {
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Tataru Taru", World: "Ultros", Datacenter: "Primal", Race: "Lalafell"}, nil
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	var buf bytes.Buffer
	h := NewCharacterCensus(ls, svc, newBufLogger(&buf)).WithWriter(mockwriter.NewFake())

	if _, err := h.Handle(context.Background(), characterPayload(42)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"handler.character_census.fetched", "handler.character_census.stored", "character_id=42", "Tataru Taru", "Ultros"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestCharacterCensus_LogsFetchError(t *testing.T) {
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, errors.New("boom")
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	var buf bytes.Buffer
	h := NewCharacterCensus(ls, svc, newBufLogger(&buf)).WithWriter(mockwriter.NewFake())

	if _, err := h.Handle(context.Background(), characterPayload(1)); err == nil {
		t.Fatal("expected error on fetch failure")
	}
	logs := buf.String()
	for _, want := range []string{"handler.character_census.fetch_error", "character_id=1", "boom"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestAchievementCensus_LogsFetchedLatest(t *testing.T) {
	ls := mocklodestone.NewFake()
	now := time.Now()
	ls.FetchAchievementsFunc = func(ctx context.Context, id uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
		return &contract.AchievementSummary{
			Milestones: []contract.AchievementResult{
				{AchievementID: 590, Name: "My Little Chocobo", Earned: true, EarnedAt: now.Add(-time.Hour)},
				{AchievementID: 999, Name: "Other", Earned: true, EarnedAt: now},
			},
			LatestAchievement: &contract.AchievementResult{AchievementID: 999, Name: "Other", Earned: true, EarnedAt: now},
		}, nil
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	if err := svc.SyncMilestones(context.Background()); err != nil {
		t.Fatalf("SyncMilestones: %v", err)
	}
	var buf bytes.Buffer
	h := NewAchievementCensus(ls, svc, newBufLogger(&buf)).WithWriter(mockwriter.NewFake())

	if _, err := h.Handle(context.Background(), achievementPayload(123)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"handler.achievement_census.complete", "milestones=1"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestIDSweep_LogsRealTimeProbesAndDiscoveries(t *testing.T) {
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		if id == 10 {
			return &contract.CharacterProfile{ID: 10, Name: "Alisaie Leveilleur", World: "Louisoix", Race: "Elezen"}, nil
		}
		return nil, contract.ErrCharacterNotFound
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	var buf bytes.Buffer
	h := NewIDSweep(ls, svc, newBufLogger(&buf)).WithWriter(mockwriter.NewFake())

	payload, _ := json.Marshal(IDSweepPayload{From: 9, To: 11})
	if _, err := h.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{
		"handler.id_sweep.start",
		"from=9",
		"to=11",
		"count=3",
		"handler.id_sweep.probe",
		"character_id=9",
		"status=not_found",
		"handler.id_sweep.discovered",
		"character_id=10",
		"Alisaie Leveilleur",
		"Louisoix",
		"character_id=11",
		"handler.id_sweep.done",
		"discovered=1",
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
}

func TestSuccessfulHandlersTraceAtInfo(t *testing.T) {
	// At Info level, successful handler runs trace the range lifecycle and
	// every discovered character; not-found probes stay Debug-only.
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Quiet Hero", World: "Ultros", Datacenter: "Primal", Race: "Hyur", Tribe: "Midlander", Gender: 1}, nil
	}
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())

	var buf bytes.Buffer
	infoLogger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := NewIDSweep(ls, svc, infoLogger).WithWriter(mockwriter.NewFake())

	payload, _ := json.Marshal(IDSweepPayload{From: 1, To: 1})
	if _, err := h.Handle(context.Background(), payload); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	logs := buf.String()
	for _, want := range []string{"handler.id_sweep.start", "handler.id_sweep.discovered", "handler.id_sweep.done", "character_id=1"} {
		if !strings.Contains(logs, want) {
			t.Errorf("Info logger should emit %q:\n%s", want, logs)
		}
	}
	for _, notWant := range []string{"handler.id_sweep.probe"} {
		if strings.Contains(logs, notWant) {
			t.Errorf("Info logger should not emit %q for a found character:\n%s", notWant, logs)
		}
	}
}
