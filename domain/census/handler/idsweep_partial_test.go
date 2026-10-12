package handler

import (
	"context"
	"errors"
	"testing"

	mockwriter "github.com/mihaiflorentin88/ffxiv-census/mock/writer"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// failingFetch serves good profiles except at failID, which returns err.
func failingFetch(failID uint32, err error) func(context.Context, uint32) (*contract.CharacterProfile, error) {
	good := sweepFetch(0, 0)
	return func(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
		if id == failID {
			return nil, err
		}
		return good(ctx, id)
	}
}

func TestIDSweep_WithWriterTransientFetchErrorStoresCharactersFoundSoFar(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	transient := errors.New("lodestone 503")
	ls.FetchCharacterFunc = failingFetch(3, transient)

	next, err := h.Handle(context.Background(), idsweepPayload(1, 5))
	if !errors.Is(err, transient) {
		t.Fatalf("Handle error = %v, want the original fetch error so the job retries", err)
	}
	if len(next) != 0 {
		t.Fatalf("chained %d jobs from a failed delivery", len(next))
	}
	got := w.Characters()
	if len(got) != 2 || got[0].Record.ID != 1 || got[1].Record.ID != 2 {
		t.Fatalf("writer characters = %+v, want ids 1 and 2 found before the error", got)
	}
}

func TestIDSweep_WithWriterTransientFetchErrorWithNothingFoundWritesNothing(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	h = h.WithWriter(w)
	transient := errors.New("lodestone 503")
	ls.FetchCharacterFunc = failingFetch(1, transient)

	if _, err := h.Handle(context.Background(), idsweepPayload(1, 3)); !errors.Is(err, transient) {
		t.Fatalf("Handle error = %v, want %v", err, transient)
	}
	if n := len(w.Results()); n != 0 {
		t.Fatalf("writer received %d results, want 0", n)
	}
}

func TestIDSweep_WithWriterTransientFetchErrorIsReturnedEvenIfPartialWriteFails(t *testing.T) {
	h, ls, _ := newTestIDSweep(t)
	w := mockwriter.NewFake()
	w.WriteErr = errors.New("commit failed")
	h = h.WithWriter(w)
	transient := errors.New("lodestone 503")
	ls.FetchCharacterFunc = failingFetch(2, transient)

	if _, err := h.Handle(context.Background(), idsweepPayload(1, 3)); !errors.Is(err, transient) {
		t.Fatalf("Handle error = %v, want the fetch error", err)
	}
}
