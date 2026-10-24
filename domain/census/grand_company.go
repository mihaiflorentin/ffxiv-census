package census

import (
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// GrandCompanyNone is the Grand Company group key of characters in no
// company. It is a group of its own so the shares add up to 100%.
const GrandCompanyNone = "None"

// The Grand Companies as the Lodestone names them: the keys of the
// grand_company groups. Every reader names a company through these.
const (
	GrandCompanyMaelstrom      = "Maelstrom"
	GrandCompanyTwinAdder      = "Order of the Twin Adder"
	GrandCompanyImmortalFlames = "Immortal Flames"
)

// GrandCompany is one Grand Company: its Lodestone Name, the Short name
// pages use in tight spots ("Twin Adder"), and the Slug of its colour token
// (--color-gc-<slug>).
type GrandCompany struct {
	Name, Short, Slug string
}

// grandCompanies lists the companies in the game's own order (Limsa Lominsa,
// Gridania, Ul'dah), with "None" last.
var grandCompanies = []GrandCompany{
	{Name: GrandCompanyMaelstrom, Short: "Maelstrom", Slug: "maelstrom"},
	{Name: GrandCompanyTwinAdder, Short: "Twin Adder", Slug: "twin-adder"},
	{Name: GrandCompanyImmortalFlames, Short: "Immortal Flames", Slug: "immortal-flames"},
	{Name: GrandCompanyNone, Short: GrandCompanyNone, Slug: "none"},
}

// GrandCompanies returns the three Grand Companies in the game's order
// ("None" left out).
func GrandCompanies() []GrandCompany {
	return append([]GrandCompany(nil), grandCompanies[:3]...)
}

// GrandCompanyShare is one company's figures within a selection.
type GrandCompanyShare struct {
	Name string
	Slug string
	// Total and Active count the company's members (active: inside the activity window).
	Total, Active int64
	// Share is Total over the selection's total.
	Share float64
	// ActiveShare is Active over Total: how many of the company's members still play.
	ActiveShare float64
	// ShareOfActive is Active over the selection's active players.
	ShareOfActive float64
}

// GrandCompanySplit is how a selection's characters split across the Grand
// Companies. Companies always holds the three companies and "None", in order.
type GrandCompanySplit struct {
	Total, Active int64
	Companies     []GrandCompanyShare
}

// Leader is the company (never "None") with the most members, or a zero
// share when no company has any.
func (s GrandCompanySplit) Leader() GrandCompanyShare {
	var lead GrandCompanyShare
	for _, c := range s.Companies {
		if c.Name != GrandCompanyNone && c.Total > lead.Total {
			lead = c
		}
	}
	return lead
}

// GrandCompanySplitFor returns the Grand Company split of a stats scope.
func GrandCompanySplitFor(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) GrandCompanySplit {
	counts := map[string]contract.GroupCount{}
	for _, g := range SnapshotGroups(snapshot, "grand_company", scope) {
		counts[g.Key] = g
	}
	return newGrandCompanySplit(counts)
}

// RaceGrandCompanyLean is how one race's characters split across the Grand
// Companies, across the whole game.
type RaceGrandCompanyLean struct {
	Race string
	GrandCompanySplit
}

// GrandCompanyRaceLean returns each race's Grand Company split (global only),
// in the game's race order with unknown races left out.
func GrandCompanyRaceLean(snapshot *contract.UIStatsSnapshot) []RaceGrandCompanyLean {
	byRace := map[string]map[string]contract.GroupCount{}
	for _, g := range SnapshotGroups(snapshot, "grand_company_race", contract.StatsScope{}) {
		company, race, ok := strings.Cut(g.Key, "|")
		if !ok || RaceRank(race) == 0 {
			continue
		}
		if byRace[race] == nil {
			byRace[race] = map[string]contract.GroupCount{}
		}
		byRace[race][company] = g
	}
	var out []RaceGrandCompanyLean
	for _, race := range raceOrder {
		if counts, ok := byRace[race]; ok {
			out = append(out, RaceGrandCompanyLean{Race: race, GrandCompanySplit: newGrandCompanySplit(counts)})
		}
	}
	return out
}

func newGrandCompanySplit(counts map[string]contract.GroupCount) GrandCompanySplit {
	var split GrandCompanySplit
	for _, gc := range grandCompanies {
		split.Total += counts[gc.Name].Total
		split.Active += counts[gc.Name].Active
	}
	for _, gc := range grandCompanies {
		c := counts[gc.Name]
		split.Companies = append(split.Companies, GrandCompanyShare{
			Name: gc.Name, Slug: gc.Slug, Total: c.Total, Active: c.Active,
			Share:         ratio(c.Total, split.Total),
			ActiveShare:   ratio(c.Active, c.Total),
			ShareOfActive: ratio(c.Active, split.Active),
		})
	}
	return split
}

func ratio(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole)
}
