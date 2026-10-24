package census

import (
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// RankingsMemo keeps the boards built from the current snapshot, so each
// board is built once per snapshot rather than on every request. A snapshot
// is identified by its generation time and schema version: the UI stats
// service hands out copies, never the same pointer twice. Safe for
// concurrent use.
type RankingsMemo struct {
	mu          sync.Mutex
	generatedAt time.Time
	schema      int
	boards      map[RankingQuery]RankingBoard
}

// NewRankingsMemo returns an empty memo.
func NewRankingsMemo() *RankingsMemo {
	return &RankingsMemo{}
}

// Board returns Rankings(snapshot, query), building it only when this
// snapshot has not produced that board yet. The caller owns the returned rows.
func (m *RankingsMemo) Board(snapshot *contract.UIStatsSnapshot, query RankingQuery) RankingBoard {
	if snapshot == nil {
		return Rankings(snapshot, query)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.boards == nil || !m.generatedAt.Equal(snapshot.GeneratedAt) || m.schema != snapshot.SchemaVersion {
		m.generatedAt, m.schema = snapshot.GeneratedAt, snapshot.SchemaVersion
		m.boards = map[RankingQuery]RankingBoard{}
	}
	board, ok := m.boards[query]
	if !ok {
		board = Rankings(snapshot, query)
		m.boards[query] = board
	}
	board.Rows = append([]RankingRow(nil), board.Rows...)
	return board
}
