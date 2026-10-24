package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ClaimQueryForTest exposes the assembled claim statement and its bind
// arguments so tests can inspect the plan PostgreSQL builds for it.
func (r *ProxyRepository) ClaimQueryForTest(queue contract.ScanQueue, limit int) (string, []any, error) {
	return r.claimQuery(queue, limit, "test-token")
}

// ReadFreeCompanyBoardsForTest runs the full refresh's Free Company pass
// inside tx, so tests can inspect the transaction afterwards.
func ReadFreeCompanyBoardsForTest(ctx context.Context, tx *sql.Tx, activitySince, builtAt time.Time) (*contract.StatsFreeCompanies, error) {
	return readFreeCompanyBoards(ctx, tx, activitySince, builtAt)
}

// FreeCompanyNamesQueryForTest is the statement that reads the kept Free
// Companies' names, for plan checks.
const FreeCompanyNamesQueryForTest = freeCompanyNamesQuery
