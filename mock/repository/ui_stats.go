package repository

import (
	"context"
	"errors"
	"sync"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ErrNoFreeCompanies is what the fake answers when it holds no Free Company
// row and no FreeCompaniesErr was set.
var ErrNoFreeCompanies = errors.New("no Free Company boards")

type UIStatsRepository struct {
	mu sync.Mutex

	Snapshot *contract.UIStatsSnapshot
	// FreeCompanies is the Free Company row LoadFreeCompanies returns;
	// FreeCompaniesErr, when set, is returned instead.
	FreeCompanies      *contract.StatsFreeCompanies
	FreeCompaniesErr   error
	FreeCompaniesCalls int
	LoadErr            error
	RefreshErr         error
	Skipped            bool
	LoadCalls          int
	RefreshCalls       int
	LastRefreshOptions contract.UIStatsRefreshOptions
}

func NewUIStatsFake(snapshot *contract.UIStatsSnapshot) *UIStatsRepository {
	return &UIStatsRepository{Snapshot: cloneStatsSnapshot(snapshot)}
}

func (f *UIStatsRepository) LoadCurrent(context.Context) (*contract.UIStatsSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LoadCalls++
	return cloneStatsSnapshot(f.Snapshot), f.LoadErr
}

func (f *UIStatsRepository) LoadFreeCompanies(context.Context) (*contract.StatsFreeCompanies, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FreeCompaniesCalls++
	if f.FreeCompaniesErr != nil {
		return nil, f.FreeCompaniesErr
	}
	if f.FreeCompanies == nil {
		return nil, ErrNoFreeCompanies
	}
	clone := *f.FreeCompanies
	clone.Worlds = append([]contract.WorldFreeCompanies(nil), f.FreeCompanies.Worlds...)
	return &clone, nil
}

func (f *UIStatsRepository) Refresh(_ context.Context, opts contract.UIStatsRefreshOptions) (*contract.UIStatsRefreshResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RefreshCalls++
	f.LastRefreshOptions = opts
	if f.RefreshErr != nil {
		return nil, f.RefreshErr
	}
	mode := opts.Mode
	if mode == "" {
		mode = contract.UIStatsRefreshFull
	}
	return &contract.UIStatsRefreshResult{Snapshot: cloneStatsSnapshot(f.Snapshot), Skipped: f.Skipped, Mode: mode}, nil
}

func cloneStatsSnapshot(snapshot *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	if snapshot == nil {
		return nil
	}
	clone := *snapshot
	clone.Groups = append([]contract.ScopedGroupCount(nil), snapshot.Groups...)
	clone.Expansions = append([]contract.ScopedExpansionCount(nil), snapshot.Expansions...)
	clone.NewCharacters = append([]contract.ScopedDailyCount(nil), snapshot.NewCharacters...)
	return &clone
}

var _ contract.UIStatsRepository = (*UIStatsRepository)(nil)
