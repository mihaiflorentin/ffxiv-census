package census

import (
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// memoSnapshot carries every section the memo derives from: location and
// privacy counts, Grand Company groups and the job section.
func memoSnapshot() *contract.UIStatsSnapshot {
	s := privacySnapshot()
	s.Groups = append(s.Groups, gcSnapshot().Groups...)
	s.Jobs = armourySnapshot().Jobs
	return s
}

// emptied is the same snapshot identity (generation time and schema) with
// none of the figures: a memo answering from it must be answering from
// what it remembered.
func emptied(s *contract.UIStatsSnapshot) *contract.UIStatsSnapshot {
	return &contract.UIStatsSnapshot{SchemaVersion: s.SchemaVersion, GeneratedAt: s.GeneratedAt, PrivacyBuiltAt: s.PrivacyBuiltAt}
}

func TestSnapshotMemoMatchesTheDirectBuilders(t *testing.T) {
	m := NewSnapshotMemo()
	s := memoSnapshot()
	eu := contract.StatsScope{Region: "EU"}
	query := RankingQuery{Metric: MetricPrivacy, By: LocationWorld}

	if got, want := m.Rankings(s, query), Rankings(s, query); !reflect.DeepEqual(got, want) {
		t.Errorf("Rankings = %+v, want %+v", got, want)
	}
	if got, want := m.Armoury(s, eu), ArmouryFor(s, eu); !reflect.DeepEqual(got, want) {
		t.Errorf("Armoury = %+v, want %+v", got, want)
	}
	if got, want := m.GrandCompanySplit(s, eu), GrandCompanySplitFor(s, eu); !reflect.DeepEqual(got, want) {
		t.Errorf("GrandCompanySplit = %+v, want %+v", got, want)
	}
	if got, want := m.GrandCompanyRaceLean(s), GrandCompanyRaceLean(s); !reflect.DeepEqual(got, want) {
		t.Errorf("GrandCompanyRaceLean = %+v, want %+v", got, want)
	}
	if got, want := m.Privacy(s, eu), PrivacyFor(s, eu); got != want {
		t.Errorf("Privacy = %+v, want %+v", got, want)
	}
	if got, want := m.PrivacyRegions(s), PrivacyRegions(s); !reflect.DeepEqual(got, want) {
		t.Errorf("PrivacyRegions = %+v, want %+v", got, want)
	}
	if got, want := m.PrivacyWorlds(s, "", 3), PrivacyWorlds(s, "", 3); !reflect.DeepEqual(got, want) {
		t.Errorf("PrivacyWorlds = %+v, want %+v", got, want)
	}
}

func TestSnapshotMemoBuildsOncePerSnapshot(t *testing.T) {
	m := NewSnapshotMemo()
	s := memoSnapshot()
	eu := contract.StatsScope{Region: "EU"}
	query := RankingQuery{Metric: MetricPrivacy, By: LocationWorld}
	rankings, armoury := m.Rankings(s, query), m.Armoury(s, eu)
	split, lean := m.GrandCompanySplit(s, eu), m.GrandCompanyRaceLean(s)
	privacy, regions, worlds := m.Privacy(s, eu), m.PrivacyRegions(s), m.PrivacyWorlds(s, "", 3)

	// Same identity, no figures: every answer comes from the memo.
	same := emptied(s)
	if got := m.Rankings(same, query); !reflect.DeepEqual(got, rankings) {
		t.Errorf("Rankings rebuilt for the same snapshot: %+v", got)
	}
	if got := m.Armoury(same, eu); !reflect.DeepEqual(got, armoury) {
		t.Errorf("Armoury rebuilt for the same snapshot: %+v", got)
	}
	if got := m.GrandCompanySplit(same, eu); !reflect.DeepEqual(got, split) {
		t.Errorf("GrandCompanySplit rebuilt for the same snapshot: %+v", got)
	}
	if got := m.GrandCompanyRaceLean(same); !reflect.DeepEqual(got, lean) {
		t.Errorf("GrandCompanyRaceLean rebuilt for the same snapshot: %+v", got)
	}
	if got := m.Privacy(same, eu); got != privacy {
		t.Errorf("Privacy rebuilt for the same snapshot: %+v", got)
	}
	if got := m.PrivacyRegions(same); !reflect.DeepEqual(got, regions) {
		t.Errorf("PrivacyRegions rebuilt for the same snapshot: %+v", got)
	}
	if got := m.PrivacyWorlds(same, "", 3); !reflect.DeepEqual(got, worlds) {
		t.Errorf("PrivacyWorlds rebuilt for the same snapshot: %+v", got)
	}

	// A new generation time, or a new schema, starts over.
	newer := emptied(s)
	newer.GeneratedAt = s.GeneratedAt.Add(time.Minute)
	if got := m.Armoury(newer, eu); got.HasData() {
		t.Errorf("Armoury for a newer snapshot = %+v, want it rebuilt (empty)", got)
	}
	v2 := emptied(s)
	v2.SchemaVersion = 2
	if got := m.Privacy(v2, eu); got.Total != 0 {
		t.Errorf("Privacy for another schema = %+v, want it rebuilt (empty)", got)
	}
}

func TestSnapshotMemoRemembersOnlyKnownSelections(t *testing.T) {
	m := NewSnapshotMemo()
	s := memoSnapshot()
	for _, scope := range []contract.StatsScope{{World: "Nowhere"}, {World: "balmung"}, {Datacenter: "Nowhere"}, {Region: "XX"}} {
		m.Armoury(s, scope)
		m.GrandCompanySplit(s, scope)
		m.Privacy(s, scope)
	}
	m.Rankings(s, RankingQuery{Metric: "bogus", By: LocationWorld})
	m.Rankings(s, RankingQuery{Metric: MetricPrivacy, By: LocationRegion})
	m.Rankings(s, RankingQuery{Metric: MetricPrivacy, By: LocationWorld, Region: "Nowhere"})
	m.PrivacyWorlds(s, "Nowhere", 3)
	m.PrivacyWorlds(s, "", 100000)
	if n := m.size(); n != 0 {
		t.Errorf("unknown selections memoised %d entries, want 0", n)
	}
	m.Armoury(s, contract.StatsScope{World: "Balmung"})
	m.Rankings(s, RankingQuery{Metric: MetricPrivacy, By: LocationWorld, Region: "EU"})
	if n := m.size(); n != 2 {
		t.Errorf("known selections memoised %d entries, want 2", n)
	}
}

func TestSnapshotMemoHandsOutCopies(t *testing.T) {
	m := NewSnapshotMemo()
	s := memoSnapshot()
	eu := contract.StatsScope{Region: "EU"}
	query := RankingQuery{Metric: MetricPrivacy, By: LocationWorld}

	board := m.Rankings(s, query)
	board.Rows[0].Key = "changed"
	armoury := m.Armoury(s, eu)
	armoury.Roles[0].Jobs[0].Levelled = -1
	split := m.GrandCompanySplit(s, eu)
	split.Companies[0].Total = -1
	lean := m.GrandCompanyRaceLean(s)
	lean[0].Companies[0].Total = -1
	regions := m.PrivacyRegions(s)
	regions[0].Key = "changed"
	worlds := m.PrivacyWorlds(s, "", 3)
	worlds.MostPrivate[0].Key = "changed"

	if !reflect.DeepEqual(m.Rankings(s, query), Rankings(s, query)) ||
		!reflect.DeepEqual(m.Armoury(s, eu), ArmouryFor(s, eu)) ||
		!reflect.DeepEqual(m.GrandCompanySplit(s, eu), GrandCompanySplitFor(s, eu)) ||
		!reflect.DeepEqual(m.GrandCompanyRaceLean(s), GrandCompanyRaceLean(s)) ||
		!reflect.DeepEqual(m.PrivacyRegions(s), PrivacyRegions(s)) ||
		!reflect.DeepEqual(m.PrivacyWorlds(s, "", 3), PrivacyWorlds(s, "", 3)) {
		t.Error("a caller's change reached the memo")
	}
}

func TestSnapshotMemoWithoutASnapshotBuildsDirectly(t *testing.T) {
	m := NewSnapshotMemo()
	if got := m.Rankings(nil, RankingQuery{Metric: MetricPopulation, By: LocationWorld}); got.Ready {
		t.Errorf("Rankings(nil) = %+v, want not ready", got)
	}
	if got := m.Armoury(nil, contract.StatsScope{}); got.HasData() {
		t.Errorf("Armoury(nil) = %+v", got)
	}
	if n := m.size(); n != 0 {
		t.Errorf("a nil snapshot memoised %d entries", n)
	}
}
