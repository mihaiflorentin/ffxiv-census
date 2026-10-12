package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// CharacterCensus re-censuses a known character on The Lodestone — the sole
// data source and the sole authority for existence. On success, chains an
// achievement-census job; on a genuine Lodestone 404, marks the character
// deleted.
type CharacterCensus struct {
	lodestone contract.LodestoneClient
	census    *census.Service
	logger    contract.Logger
	writerSlot
}

// WithWriter routes this handler's storage through w and returns the handler.
func (h *CharacterCensus) WithWriter(w contract.ResultWriter) *CharacterCensus {
	h.set(w)
	return h
}

func NewCharacterCensus(
	lodestone contract.LodestoneClient,
	svc *census.Service,
	logger contract.Logger,
) *CharacterCensus {
	return &CharacterCensus{
		lodestone: lodestone,
		census:    svc,
		logger:    loggerOrDiscard(logger),
	}
}

func (h *CharacterCensus) Handle(ctx context.Context, payload []byte) ([]contract.QueueJob, error) {
	var p CharacterCensusPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, errors.Join(contract.ErrPoisonPayload, fmt.Errorf("character-census payload: %w", err))
	}
	h.logger.DebugContext(ctx, "handler.character_census", slog.Uint64("character_id", uint64(p.CharacterID)))

	if h.lodestone == nil {
		return nil, errors.New("lodestone client unconfigured on this worker")
	}

	if err := h.require(); err != nil {
		return nil, err
	}

	char, err := h.lodestone.FetchCharacter(ctx, p.CharacterID)
	if err == nil {
		return h.finishLodestone(ctx, char)
	}

	if errors.Is(err, contract.ErrCharacterNotFound) {
		// The Lodestone is authoritative for existence: a genuine 404 is
		// terminal — mark the character deleted and ack.
		if derr := h.census.MarkCharacterDeleted(ctx, p.CharacterID, time.Now().UTC()); derr != nil {
			h.logger.ErrorContext(ctx, "handler.character_census.store_error", slog.Uint64("character_id", uint64(p.CharacterID)), slog.Any("error", derr))
			return nil, fmt.Errorf("character-census mark-deleted %d: %w", p.CharacterID, derr)
		}
		h.logger.DebugContext(ctx, "handler.character_census.deleted", slog.Uint64("character_id", uint64(p.CharacterID)))
		return nil, nil
	}

	// Transient Lodestone error (challenge, timeout, 429, dial): fail the
	// delivery so the queue's retry ladder re-probes it on Lodestone.
	h.logger.ErrorContext(ctx, "handler.character_census.fetch_error", slog.Uint64("character_id", uint64(p.CharacterID)), slog.String("source", "lodestone"), slog.Any("error", err))
	return nil, fmt.Errorf("character-census fetch %d: %w", p.CharacterID, err)
}

// finishLodestone stores a fetched Lodestone profile and chains dependent
// jobs. A hidden profile (census.ErrProfileHidden) completes the job without
// storing or chaining.
func (h *CharacterCensus) finishLodestone(ctx context.Context, char *contract.CharacterProfile) ([]contract.QueueJob, error) {
	h.logger.DebugContext(ctx, "handler.character_census.fetched", slog.Uint64("character_id", uint64(char.ID)), slog.String("name", char.Name), slog.String("world", char.World), slog.String("fc_id", char.FreeCompanyID), slog.String("source", "lodestone"))
	if err := h.store(ctx, char); err != nil {
		if errors.Is(err, census.ErrProfileHidden) {
			h.logger.InfoContext(ctx, "handler.character_census.skipped", slog.Uint64("character_id", uint64(char.ID)), slog.String("reason", "profile_hidden"))
			return nil, nil
		}
		h.logger.ErrorContext(ctx, "handler.character_census.store_error", slog.Uint64("character_id", uint64(char.ID)), slog.String("name", char.Name), slog.String("world", char.World), slog.Any("error", err))
		return nil, fmt.Errorf("character-census upsert %d: %w", char.ID, err)
	}
	h.logger.InfoContext(ctx, "handler.character_census.stored", slog.Uint64("character_id", uint64(char.ID)), slog.String("name", char.Name), slog.String("world", char.World))
	next := h.dependentJobs(ctx, char.ID)
	h.logger.DebugContext(ctx, "handler.character_census.done", slog.Uint64("character_id", uint64(char.ID)), slog.Int("chained", len(next)))
	return next, nil
}

// dependentJobs chains the achievement census unless it would only be skipped:
// a private flag verified within the recheck window. The refresh just stored
// leaves that flag and its verification time as they were.
func (h *CharacterCensus) dependentJobs(ctx context.Context, id uint32) []contract.QueueJob {
	due, err := h.census.AchievementCensusDue(ctx, id)
	if err != nil {
		h.logger.ErrorContext(ctx, "handler.character_census.due_check_failed", slog.Uint64("character_id", uint64(id)), slog.Any("error", err))
	}
	if !due {
		return nil
	}
	return BuildDependentCharacterJobs(id)
}

// store hands the profile to the writer and waits for its batch to commit. It
// returns census.ErrProfileHidden for a profile that must not be stored.
func (h *CharacterCensus) store(ctx context.Context, char *contract.CharacterProfile) error {
	w, err := h.census.PrepareCharacter(char)
	if err != nil {
		return err
	}
	return h.writer.Write(ctx, contract.CensusResult{Characters: []contract.CharacterWrite{w}})
}
