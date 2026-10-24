package census

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// FreeCompaniesReady reports whether snapshot names a nightly build of the
// Free Company boards: a schema with the section and a build time.
func FreeCompaniesReady(snapshot *contract.UIStatsSnapshot) bool {
	return SnapshotHasSection(snapshot, SectionFreeCompanies)
}

// FreeCompanyBoardResult is a board as the service hands it out. Ready is
// false while there are no boards to show (a snapshot from before them, or a
// missing row): the page shows its "arrives with the next refresh" state.
type FreeCompanyBoardResult struct {
	FreeCompanyBoard
	Ready bool
	// BuiltAt is the nightly rebuild that produced the boards.
	BuiltAt time.Time
}

// freeCompanyCache holds the Free Company row and the boards derived from
// it. The row is decoded only when the main snapshot names a new build time,
// and each board is built once per row.
type freeCompanyCache struct {
	mu sync.Mutex
	// builtAt is the main snapshot build time the row was loaded for.
	builtAt time.Time
	row     *contract.StatsFreeCompanies
	boards  map[FreeCompanyQuery]FreeCompanyBoard
}

// FreeCompanyBoard returns the Free Company board query asks for, from the
// boards the full refresh named in snapshot (its FreeCompaniesBuiltAt). The
// row is loaded only when that build time changes.
func (s *UIStatsService) FreeCompanyBoard(ctx context.Context, snapshot *contract.UIStatsSnapshot, query FreeCompanyQuery) (FreeCompanyBoardResult, error) {
	result := FreeCompanyBoardResult{FreeCompanyBoard: FreeCompanyBoard{Query: query}}
	if s == nil || s.repo == nil || !FreeCompaniesReady(snapshot) {
		return result, nil
	}
	c := &s.freeCompanies
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.row == nil || !c.builtAt.Equal(snapshot.FreeCompaniesBuiltAt) {
		row, err := s.repo.LoadFreeCompanies(ctx)
		if errors.Is(err, ErrUIStatsUnavailable) {
			return result, nil
		}
		if err != nil {
			return result, fmt.Errorf("load Free Company boards: %w", err)
		}
		c.builtAt, c.row, c.boards = snapshot.FreeCompaniesBuiltAt, row, map[FreeCompanyQuery]FreeCompanyBoard{}
	}
	board, ok := c.boards[query]
	if !ok {
		board = FreeCompanyBoardFor(c.row, query)
		c.boards[query] = board
	}
	result.FreeCompanyBoard = board
	result.Rows = append([]FreeCompanyRow(nil), board.Rows...)
	result.Ready, result.BuiltAt = true, c.row.BuiltAt
	return result, nil
}
