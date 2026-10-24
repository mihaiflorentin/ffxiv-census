package census

import (
	"sort"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The Free Company boards' two judgement calls, tuned here in one place (and
// documented on the Methodology page).
const (
	// FreeCompanyBoardSize is how many Free Companies a board ranks, and how
	// many the nightly refresh keeps per world for each measure.
	FreeCompanyBoardSize = 100
	// FreeCompanyMinimumMembers is the fewest members seen a Free Company
	// needs to be ranked at all.
	FreeCompanyMinimumMembers = 2
)

// FreeCompanyMeasure is what a Free Company board ranks by.
type FreeCompanyMeasure string

const (
	// FreeCompanyMembers ranks by members seen by the census.
	FreeCompanyMembers FreeCompanyMeasure = "members"
	// FreeCompanyActive ranks by active members (a public achievement inside
	// the activity window).
	FreeCompanyActive FreeCompanyMeasure = "active"
)

// FreeCompanyQuery selects one Free Company board: a measure and a scope
// (the whole game, a region, a datacenter or a world).
type FreeCompanyQuery struct {
	Measure FreeCompanyMeasure
	Scope   contract.StatsScope
}

// FreeCompanyRow is one entry of a Free Company board.
type FreeCompanyRow struct {
	// Rank counts from 1 in board order.
	Rank int
	// ID is the Lodestone Free Company id.
	ID, Name          string
	World, Datacenter string
	Members, Active   int64
	// Value is the board's measure (Members or Active) and ShareOfLeader
	// that value over the leader's (0 to 1).
	Value         int64
	ShareOfLeader float64
}

// FreeCompanyBoard is an ordered Free Company board.
type FreeCompanyBoard struct {
	Query FreeCompanyQuery
	Rows  []FreeCompanyRow
}

// FreeCompanyBoardFor builds the board query asks for: the top
// FreeCompanyBoardSize Free Companies of the scope by the measure, merged
// from the per-world lists. The merge is exact because each Free Company
// lives on one world, so a scope's top entries are among its worlds' top
// entries. Ties break by the other measure, then name, then id.
func FreeCompanyBoardFor(boards *contract.StatsFreeCompanies, query FreeCompanyQuery) FreeCompanyBoard {
	board := FreeCompanyBoard{Query: query}
	if boards == nil {
		return board
	}
	for _, w := range boards.Worlds {
		if !ScopeHasWorld(query.Scope, w.World) {
			continue
		}
		dc := WorldDatacenter(w.World)
		for _, c := range w.Companies {
			row := FreeCompanyRow{ID: c.ID, Name: c.Name, World: w.World, Datacenter: dc, Members: c.Members, Active: c.Active, Value: c.Members}
			if query.Measure == FreeCompanyActive {
				row.Value = c.Active
			}
			board.Rows = append(board.Rows, row)
		}
	}
	sort.Slice(board.Rows, func(i, j int) bool { return freeCompanyRowLess(board.Rows[i], board.Rows[j]) })
	if len(board.Rows) > FreeCompanyBoardSize {
		board.Rows = board.Rows[:FreeCompanyBoardSize]
	}
	for i := range board.Rows {
		board.Rows[i].Rank = i + 1
		board.Rows[i].ShareOfLeader = ratio(board.Rows[i].Value, board.Rows[0].Value)
	}
	return board
}

// freeCompanyRowLess orders a board: the measure, then the other measure
// (members + active - value), then name, then id.
func freeCompanyRowLess(a, b FreeCompanyRow) bool {
	if a.Value != b.Value {
		return a.Value > b.Value
	}
	if otherA, otherB := a.Members+a.Active-a.Value, b.Members+b.Active-b.Value; otherA != otherB {
		return otherA > otherB
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if len(a.ID) != len(b.ID) {
		return len(a.ID) < len(b.ID)
	}
	return a.ID < b.ID
}
