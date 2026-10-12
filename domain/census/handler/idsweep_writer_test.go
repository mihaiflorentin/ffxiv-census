package handler

import (
	"context"
	"errors"
	"testing"

	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func sweepFetch(notFound, hidden uint32) func(context.Context, uint32) (*contract.CharacterProfile, error) {
	return func(_ context.Context, id uint32) (*contract.CharacterProfile, error) {
		switch id {
		case notFound:
			return nil, contract.ErrCharacterNotFound
		case hidden:
			return &contract.CharacterProfile{ID: id, Name: "Hidden", World: "Ultros", Datacenter: "Primal"}, nil
		}
		return &contract.CharacterProfile{
			ID: id, Name: "Char", World: "Ultros", Datacenter: "Primal", Race: "Hyur",
			ClassJobs: []contract.ClassJobRecord{{ClassJobID: 19, Name: "Paladin", Level: 100}},
		}, nil
	}
}

func TestIDSweep_WithWriterHandsDiscoveredCharactersToWriter(t *testing.T) {
	h, ls, chars := newTestIDSweep(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(2, 4)

	next, err := h.Handle(context.Background(), idsweepPayload(1, 4))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	got := w.Characters()
	if len(got) != 2 || got[0].Record.ID != 1 || got[1].Record.ID != 3 {
		t.Fatalf("writer characters = %+v, want ids 1 and 3 (2 not found, 4 hidden)", got)
	}
	if len(got[0].Jobs) != 1 || got[0].Jobs[0].ClassJobID != 19 {
		t.Fatalf("jobs = %+v, want the Paladin job", got[0].Jobs)
	}
	if len(next) != 2 {
		t.Fatalf("chained jobs = %d, want 2", len(next))
	}
	if chars.UpsertCalls != 0 {
		t.Fatalf("repository written directly %d times; with a writer all storage goes through it", chars.UpsertCalls)
	}
}

func TestIDSweep_WithWriterWaitsForOneBatchPerJob(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(0, 0)

	if _, err := h.Handle(context.Background(), idsweepPayload(1, 5)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(w.Results()); n != 1 {
		t.Fatalf("writer received %d results, want 1 (all characters of a job in one result)", n)
	}
}

func TestIDSweep_WithWriterFailureFailsTheJobAndChainsNothing(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	boom := errors.New("commit failed")
	w.WriteErr = boom
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = sweepFetch(0, 0)

	next, err := h.Handle(context.Background(), idsweepPayload(1, 3))
	if !errors.Is(err, boom) {
		t.Fatalf("Handle error = %v, want %v", err, boom)
	}
	if len(next) != 0 {
		t.Fatalf("chained %d jobs for characters that were never stored", len(next))
	}
}

func TestIDSweep_WithWriterNothingFoundWritesNothing(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	ls.FetchCharacterFunc = func(context.Context, uint32) (*contract.CharacterProfile, error) {
		return nil, contract.ErrCharacterNotFound
	}
	if _, err := h.Handle(context.Background(), idsweepPayload(1, 3)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := len(w.Results()); n != 0 {
		t.Fatalf("writer received %d results, want 0", n)
	}
}
