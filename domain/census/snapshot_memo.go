package census

import (
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// SnapshotMemo keeps the boards and splits derived from the current
// snapshot, so each is built once per snapshot rather than on every
// request: the Rankings boards, the Armoury, the Grand Company split and
// race lean, and the privacy figures. A snapshot is identified by its
// generation time and schema version (the UI stats service hands out
// copies, never the same pointer twice); a new identity drops everything.
// Only known selections are remembered (KnownScope, a known metric or
// region), so request filters cannot grow it; anything else is built and
// handed back without being stored. Every result is the caller's own copy.
// Safe for concurrent use.
type SnapshotMemo struct {
	mu          sync.Mutex
	generatedAt time.Time
	schema      int
	held        bool
	entries     memoEntries
}

// memoEntries are the derived values of one snapshot.
type memoEntries struct {
	rankings       map[RankingQuery]RankingBoard
	armoury        map[contract.StatsScope]Armoury
	split          map[contract.StatsScope]GrandCompanySplit
	privacy        map[contract.StatsScope]PrivacyShare
	privacyWorlds  map[privacyWorldsKey]PrivacyBoard
	lean           []RaceGrandCompanyLean
	leanHeld       bool
	privacyRegions []PrivacyShare
	regionsHeld    bool
	worldNew       map[string]NewCharacterTotals
	worldNewBase   NewCharacterTotals
	worldNewHeld   bool
}

type privacyWorldsKey struct {
	region string
	n      int
}

// NewSnapshotMemo returns an empty memo.
func NewSnapshotMemo() *SnapshotMemo {
	return &SnapshotMemo{}
}

// size is how many values the memo holds.
func (m *SnapshotMemo) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries
	n := len(e.rankings) + len(e.armoury) + len(e.split) + len(e.privacy) + len(e.privacyWorlds)
	if e.leanHeld {
		n++
	}
	if e.regionsHeld {
		n++
	}
	if e.worldNewHeld {
		n++
	}
	return n
}

// lockFor locks the memo and makes it hold snapshot's values, dropping those
// of any other snapshot. The caller unlocks.
func (m *SnapshotMemo) lockFor(snapshot *contract.UIStatsSnapshot) {
	m.mu.Lock()
	if !m.held || !m.generatedAt.Equal(snapshot.GeneratedAt) || m.schema != snapshot.SchemaVersion {
		m.generatedAt, m.schema, m.held = snapshot.GeneratedAt, snapshot.SchemaVersion, true
		m.entries = memoEntries{}
	}
}

// memoised returns table[key] for snapshot, building and storing it when
// missing; remember false builds without storing. table picks the map out
// of the entries. The value returned is the stored one: callers copy it.
func memoised[K comparable, V any](m *SnapshotMemo, snapshot *contract.UIStatsSnapshot, remember bool, table func(*memoEntries) *map[K]V, key K, build func() V) V {
	if snapshot == nil || !remember {
		return build()
	}
	m.lockFor(snapshot)
	defer m.mu.Unlock()
	t := table(&m.entries)
	if *t == nil {
		*t = map[K]V{}
	}
	if v, ok := (*t)[key]; ok {
		return v
	}
	v := build()
	(*t)[key] = v
	return v
}

// Rankings returns Rankings(snapshot, query).
func (m *SnapshotMemo) Rankings(snapshot *contract.UIStatsSnapshot, query RankingQuery) RankingBoard {
	_, knownMetric := rankingMetrics[query.Metric]
	known := knownMetric && (query.By == LocationWorld || query.By == LocationDatacenter) &&
		(query.Region == "" || RegionRank(query.Region) > 0)
	board := memoised(m, snapshot, known, func(e *memoEntries) *map[RankingQuery]RankingBoard { return &e.rankings },
		query, func() RankingBoard { return Rankings(snapshot, query) })
	board.Rows = append([]RankingRow(nil), board.Rows...)
	return board
}

// Armoury returns ArmouryFor(snapshot, scope).
func (m *SnapshotMemo) Armoury(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) Armoury {
	a := memoised(m, snapshot, KnownScope(scope), func(e *memoEntries) *map[contract.StatsScope]Armoury { return &e.armoury },
		scope, func() Armoury { return ArmouryFor(snapshot, scope) })
	roles := make([]ArmouryRole, len(a.Roles))
	for i, r := range a.Roles {
		r.Jobs = append([]ArmouryJob(nil), r.Jobs...)
		roles[i] = r
	}
	if a.Roles == nil {
		roles = nil
	}
	a.Roles = roles
	return a
}

// GrandCompanySplit returns GrandCompanySplitFor(snapshot, scope).
func (m *SnapshotMemo) GrandCompanySplit(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) GrandCompanySplit {
	split := memoised(m, snapshot, KnownScope(scope), func(e *memoEntries) *map[contract.StatsScope]GrandCompanySplit { return &e.split },
		scope, func() GrandCompanySplit { return GrandCompanySplitFor(snapshot, scope) })
	split.Companies = append([]GrandCompanyShare(nil), split.Companies...)
	return split
}

// GrandCompanyRaceLean returns GrandCompanyRaceLean(snapshot).
func (m *SnapshotMemo) GrandCompanyRaceLean(snapshot *contract.UIStatsSnapshot) []RaceGrandCompanyLean {
	var lean []RaceGrandCompanyLean
	if snapshot == nil {
		lean = GrandCompanyRaceLean(snapshot)
	} else {
		m.lockFor(snapshot)
		if !m.entries.leanHeld {
			m.entries.lean, m.entries.leanHeld = GrandCompanyRaceLean(snapshot), true
		}
		lean = m.entries.lean
		m.mu.Unlock()
	}
	if lean == nil {
		return nil
	}
	out := make([]RaceGrandCompanyLean, len(lean))
	for i, l := range lean {
		l.Companies = append([]GrandCompanyShare(nil), l.Companies...)
		out[i] = l
	}
	return out
}

// Privacy returns PrivacyFor(snapshot, scope).
func (m *SnapshotMemo) Privacy(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) PrivacyShare {
	return memoised(m, snapshot, KnownScope(scope), func(e *memoEntries) *map[contract.StatsScope]PrivacyShare { return &e.privacy },
		scope, func() PrivacyShare { return PrivacyFor(snapshot, scope) })
}

// PrivacyRegions returns PrivacyRegions(snapshot).
func (m *SnapshotMemo) PrivacyRegions(snapshot *contract.UIStatsSnapshot) []PrivacyShare {
	var regions []PrivacyShare
	if snapshot == nil {
		regions = PrivacyRegions(snapshot)
	} else {
		m.lockFor(snapshot)
		if !m.entries.regionsHeld {
			m.entries.privacyRegions, m.entries.regionsHeld = PrivacyRegions(snapshot), true
		}
		regions = m.entries.privacyRegions
		m.mu.Unlock()
	}
	return append([]PrivacyShare(nil), regions...)
}

// PrivacyWorlds returns PrivacyWorlds(snapshot, region, n).
func (m *SnapshotMemo) PrivacyWorlds(snapshot *contract.UIStatsSnapshot, region string, n int) PrivacyBoard {
	known := (region == "" || RegionRank(region) > 0) && n > 0 && n <= len(worldDatacenters)
	board := memoised(m, snapshot, known, func(e *memoEntries) *map[privacyWorldsKey]PrivacyBoard { return &e.privacyWorlds },
		privacyWorldsKey{region, n}, func() PrivacyBoard { return PrivacyWorlds(snapshot, region, n) })
	board.MostPrivate = append([]PrivacyShare(nil), board.MostPrivate...)
	board.LeastPrivate = append([]PrivacyShare(nil), board.LeastPrivate...)
	return board
}

// WorldNewCharacters returns NewCharactersTotals(snapshot, {World: world})
// from an index of every world built once per snapshot, so a page listing
// every world reads each in constant time.
func (m *SnapshotMemo) WorldNewCharacters(snapshot *contract.UIStatsSnapshot, world string) NewCharacterTotals {
	if snapshot == nil {
		return NewCharacterTotals{}
	}
	m.lockFor(snapshot)
	defer m.mu.Unlock()
	if !m.entries.worldNewHeld {
		m.entries.worldNew, m.entries.worldNewHeld = worldNewCharacters(snapshot), true
		m.entries.worldNewBase = emptyNewCharacterTotals(SnapshotNewCharactersBounds(snapshot))
	}
	return sumWorlds(m.entries.worldNewBase, m.entries.worldNew, map[string]struct{}{CanonicalWorld(world): {}})
}
