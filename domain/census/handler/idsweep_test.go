package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mocklodestone "github.com/mihaiflorentin88/ffxiv-census/mock/lodestone"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func newTestIDSweep(t *testing.T) (*IDSweep, *mocklodestone.Fake, *mockrepo.CharacterRepository) {
	t.Helper()
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	return NewIDSweep(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, nil)), ls, chars
}

func idsweepPayload(from, to uint32) []byte {
	b, _ := json.Marshal(IDSweepPayload{From: from, To: to})
	return b
}

func idsweepPayloadWithSource(from, to uint32, source string) []byte {
	b, _ := json.Marshal(IDSweepPayload{From: from, To: to, Source: source})
	return b
}

func TestIDSweep_DiscoversAndChains(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		if id == 2 {
			return nil, contract.ErrCharacterNotFound
		}
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayload(1, 3))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("next jobs = %d, want 2 (ids 1 and 3)", len(next))
	}
	for _, j := range next {
		if j.Type != EventAchievementCensus {
			t.Errorf("job type = %q, want %q", j.Type, EventAchievementCensus)
		}
		var p AchievementCensusPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if p.CharacterID != 1 && p.CharacterID != 3 {
			t.Errorf("chained character id = %d, want 1 or 3", p.CharacterID)
		}
	}
	// 404 (id 2) must not be upserted; 1 and 3 must.
	if got, _ := chars.Get(context.Background(), 2); got != nil {
		t.Errorf("id 2 should not be upserted (404)")
	}
	if got, _ := chars.Get(context.Background(), 1); got == nil {
		t.Errorf("id 1 should be upserted")
	}
	if got, _ := chars.Get(context.Background(), 3); got == nil {
		t.Errorf("id 3 should be upserted")
	}
}

func TestIDSweep_TransientErrorReturnsError(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, errors.New("transient network error")
	}
	if _, err := h.Handle(context.Background(), idsweepPayload(1, 1)); err == nil {
		t.Fatal("expected error on transient fetch failure")
	}
}

// A transient Lodestone error must keep its typed chain: the worker classifies
// the ProxyCheckError (destination cooldown vs proxy rotation) via errors.As.
func TestIDSweep_TransientErrorUnwrapsToProxyCheckError(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, fmt.Errorf("fetch character %d: request %s: %w", id, "url", &contract.ProxyCheckError{
			Kind: contract.CheckTarget, Reason: "status 202", Challenge: true, Err: errors.New("HTTP 202"),
		})
	}

	_, err := h.Handle(context.Background(), idsweepPayload(900, 900))
	if err == nil {
		t.Fatal("expected the transient error, got nil")
	}
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || !checkErr.Challenge {
		t.Fatalf("handler error must unwrap to the typed challenge error so the worker can cool the destination, got %v", err)
	}
}

func TestIDSweep_MaxUint32DoesNotOverflow(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, contract.ErrCharacterNotFound
	}
	// A single ID at MaxUint32 must terminate, not wrap into an infinite loop.
	if _, err := h.Handle(context.Background(), idsweepPayload(math.MaxUint32, math.MaxUint32)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

func TestIDSweep_InvalidRange(t *testing.T) {
	h, _, _ := newTestIDSweep(t)
	if _, err := h.Handle(context.Background(), idsweepPayload(5, 3)); err == nil {
		t.Fatal("expected error for from > to")
	}
}

func TestIDSweep_NotFoundSkipsCharacterWithoutFailingChunk(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		if id == 75 {
			return nil, contract.ErrCharacterNotFound
		}
		return &contract.CharacterProfile{ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayload(74, 76))
	if err != nil {
		t.Fatalf("Handle should succeed when character returns ErrCharacterNotFound: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("next jobs = %d, want 2 (ids 74 and 76)", len(next))
	}
	if got, _ := chars.Get(context.Background(), 75); got != nil {
		t.Errorf("id 75 should not be upserted (non-existent)")
	}
	if got, _ := chars.Get(context.Background(), 74); got == nil {
		t.Errorf("id 74 should be upserted")
	}
	if got, _ := chars.Get(context.Background(), 76); got == nil {
		t.Errorf("id 76 should be upserted")
	}
}

func TestIDSweep_AutoSource_Success(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID:         id,
			Name:       "Lodestone Hero",
			World:      "Balmung",
			Datacenter: "Crystal",
			Race:       "Hyur",
		}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayloadWithSource(101, 101, "auto"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("next jobs = %d, want 1", len(next))
	}

	got, err := chars.Get(context.Background(), 101)
	if err != nil || got == nil {
		t.Fatalf("Get(101): %v / %+v", err, got)
	}
	if got.Name != "Lodestone Hero" || got.World != "Balmung" || got.Region != "NA" {
		t.Errorf("got %+v, want Lodestone Hero from NA", got)
	}
}

func TestIDSweep_ExplicitLodestoneSource(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID:         501,
			Name:       "Lodestone Only",
			World:      "Shinryu",
			Datacenter: "Mana",
			Race:       "Hyur",
		}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayloadWithSource(501, 501, "lodestone"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("next jobs = %d, want 1", len(next))
	}
	if got, _ := chars.Get(context.Background(), 501); got == nil {
		t.Errorf("id 501 should be upserted")
	}
}

// A legacy queued payload carrying the removed "tomestone" source must
// behave as auto (Lodestone), never fail or hang the delivery.
func TestIDSweep_LegacyTomestoneSourceBehavesAsAuto(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: id, Name: "Legacy", World: "Ultros", Datacenter: "Primal", Race: "Hyur"}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayloadWithSource(610, 610, "tomestone"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 1 {
		t.Fatalf("next jobs = %d, want 1", len(next))
	}
	if got, _ := chars.Get(context.Background(), 610); got == nil {
		t.Errorf("id 610 should be upserted")
	}
}

func TestIDSweep_NilLodestoneClient_Error(t *testing.T) {
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	h := NewIDSweep(nil, svc, nil)

	payload, _ := json.Marshal(IDSweepPayload{From: 100, To: 105})

	_, err := h.Handle(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error when the lodestone client is nil, got nil")
	}
	if !strings.Contains(err.Error(), "lodestone client unconfigured") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestIDSweep_ReturnsDownstreamJobsInNext(t *testing.T) {
	ls := mocklodestone.NewFake()
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())

	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{
			ID:            id,
			Name:          "Immediate Hero",
			World:         "Ultros",
			Datacenter:    "Primal",
			Race:          "Hyur",
			FreeCompanyID: fmt.Sprintf("fc-%d", id),
		}, nil
	}

	h := NewIDSweep(ls, svc, nil).WithWriter(mockwriter.NewStoring(chars, nil))

	next, err := h.Handle(context.Background(), idsweepPayload(1, 2))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("expected 2 returned jobs (2 ach), got %d", len(next))
	}
	for _, j := range next {
		if j.Type != EventAchievementCensus {
			t.Errorf("unexpected job type: %q", j.Type)
		}
	}
}

// Hidden profiles (no race data: private, "Access Restricted", or "----"
// race/clan) must be skipped without failing the chunk.
func TestIDSweep_HiddenProfileSkipped(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return &contract.CharacterProfile{ID: 8, Name: "Restricted", World: "Odin", Datacenter: "Chaos", Gender: 1, Race: "----"}, nil
	}

	next, err := h.Handle(context.Background(), idsweepPayload(8, 8))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(next) != 0 {
		t.Errorf("next jobs = %+v, want none for hidden profile", next)
	}
	if got, _ := chars.Get(context.Background(), 8); got != nil {
		t.Errorf("hidden-profile character was stored: %+v", got)
	}
}

func BenchmarkIDSweepNotFoundInfo(b *testing.B) {
	ls := mocklodestone.NewFake()
	ls.FetchCharacterFunc = func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		return nil, contract.ErrCharacterNotFound
	}
	chars := mockrepo.NewCharacterFake()
	svc := census.NewService(chars, mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := NewIDSweep(ls, svc, logger).WithWriter(mockwriter.NewFake())
	payload := idsweepPayload(1, 100)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		next, err := h.Handle(context.Background(), payload)
		if err != nil {
			b.Fatalf("Handle: %v", err)
		}
		if len(next) != 0 {
			b.Fatalf("expected 0 next jobs, got %d", len(next))
		}
	}
}

func TestIDSweep_DecodeErrorIsPoison(t *testing.T) {
	h, _, _ := newTestIDSweep(t)
	_, err := h.Handle(context.Background(), []byte("{not json"))
	if !errors.Is(err, contract.ErrPoisonPayload) {
		t.Fatalf("undecodable payload must wrap contract.ErrPoisonPayload, got %v", err)
	}
}

func TestIDSweep_InvalidRangeIsPoison(t *testing.T) {
	h, _, _ := newTestIDSweep(t)
	payload, _ := json.Marshal(IDSweepPayload{From: 10, To: 5})
	_, err := h.Handle(context.Background(), payload)
	if !errors.Is(err, contract.ErrPoisonPayload) {
		t.Fatalf("inverted range must wrap contract.ErrPoisonPayload, got %v", err)
	}
}
