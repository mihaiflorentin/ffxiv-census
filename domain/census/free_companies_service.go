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
	// ActivityWindowDays is the activity window the boards' active counts
	// used, in whole days (BuiltAt minus their activity cutoff). The boards
	// are rebuilt nightly, so it can differ from the main snapshot's.
	ActivityWindowDays int
}

// freeCompanyMissingRetry is how long a missing Free Company row is
// remembered before the next request looks again (a new main snapshot build
// time looks at once).
const freeCompanyMissingRetry = time.Minute

// freeCompanyCache holds the Free Company row and the boards derived from
// it. The row is loaded when the main snapshot names a new build time, by
// one caller at a time while the others wait without holding the lock; a
// missing row is remembered for freeCompanyMissingRetry. Boards are
// memoised per row for known scopes and measures only, so request filters
// cannot grow the map.
type freeCompanyCache struct {
	mu sync.Mutex
	// builtAt is the main snapshot build time the held outcome is for;
	// loaded reports there is one.
	builtAt time.Time
	loaded  bool
	// row is nil when the row was missing at missingAt.
	row       *contract.StatsFreeCompanies
	missingAt time.Time
	boards    map[FreeCompanyQuery]FreeCompanyBoard
	// loading is closed when the running load finishes; nil when idle.
	loading chan struct{}
}

// memoised is how many boards the cache holds.
func (c *freeCompanyCache) memoised() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.boards)
}

// FreeCompanyBoard returns the Free Company board query asks for, from the
// boards the full refresh named in snapshot (its FreeCompaniesBuiltAt). The
// row is loaded only when that build time changes.
func (s *UIStatsService) FreeCompanyBoard(ctx context.Context, snapshot *contract.UIStatsSnapshot, query FreeCompanyQuery) (FreeCompanyBoardResult, error) {
	result := FreeCompanyBoardResult{FreeCompanyBoard: FreeCompanyBoard{Query: query}}
	if s == nil || s.repo == nil || !FreeCompaniesReady(snapshot) {
		return result, nil
	}
	row, err := s.freeCompanyRow(ctx, snapshot.FreeCompaniesBuiltAt)
	if err != nil || row == nil {
		return result, err
	}
	board := s.freeCompanies.board(row, query)
	result.FreeCompanyBoard = board
	result.Rows = append([]FreeCompanyRow(nil), board.Rows...)
	result.Ready, result.BuiltAt = true, row.BuiltAt
	result.ActivityWindowDays = WindowDays(row.BuiltAt.Sub(row.ActivitySince))
	return result, nil
}

// freeCompanyRow returns the Free Company row for the main snapshot build
// time builtAt, loading it when the cache holds nothing usable for it. Nil
// with no error means the row is missing.
func (s *UIStatsService) freeCompanyRow(ctx context.Context, builtAt time.Time) (*contract.StatsFreeCompanies, error) {
	c := &s.freeCompanies
	for {
		c.mu.Lock()
		if c.loaded && c.builtAt.Equal(builtAt) && (c.row != nil || s.now().Sub(c.missingAt) < freeCompanyMissingRetry) {
			row := c.row
			c.mu.Unlock()
			return row, nil
		}
		if wait := c.loading; wait != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("load Free Company boards: %w", ctx.Err())
			case <-wait:
				continue
			}
		}
		done := make(chan struct{})
		c.loading = done
		c.mu.Unlock()

		row, err := s.repo.LoadFreeCompanies(ctx)
		missing := errors.Is(err, ErrUIStatsUnavailable)
		c.mu.Lock()
		c.loading = nil
		close(done)
		if err == nil || missing {
			c.builtAt, c.loaded, c.row, c.boards = builtAt, true, row, map[FreeCompanyQuery]FreeCompanyBoard{}
			if missing {
				c.row, c.missingAt = nil, s.now()
			}
		}
		c.mu.Unlock()
		switch {
		case missing:
			return nil, nil
		case err != nil:
			return nil, fmt.Errorf("load Free Company boards: %w", err)
		}
		return row, nil
	}
}

// board returns query's board of row, memoising it while row is the cached
// one and query is a known scope and measure.
func (c *freeCompanyCache) board(row *contract.StatsFreeCompanies, query FreeCompanyQuery) FreeCompanyBoard {
	c.mu.Lock()
	board, ok := c.boards[query]
	current := c.row == row
	c.mu.Unlock()
	if ok && current {
		return board
	}
	board = FreeCompanyBoardFor(row, query)
	if !KnownScope(query.Scope) || (query.Measure != FreeCompanyMembers && query.Measure != FreeCompanyActive) {
		return board
	}
	c.mu.Lock()
	if c.row == row {
		c.boards[query] = board
	}
	c.mu.Unlock()
	return board
}
