package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ClanRow is one clan with its race and share of the selection.
type ClanRow struct {
	Clan  string
	Race  string
	Total int64
	Share string
}

// RaceGenderRow is one race's character count per gender.
type RaceGenderRow struct {
	Race    string
	Male    int64
	Female  int64
	Unknown int64
}

// RacesViewData holds race demographics, filter state, and chart data for /ui/races.
type RacesViewData struct {
	TotalCharacters    int64
	ActiveCharacters   int64
	ActivePercent      string
	NewCharacters      int64
	NewCharactersTrend Trend
	SelectedRegion     string
	SelectedDC         string
	SelectedWorld      string
	// Scope reads like "across every region" or "on the Chaos datacenter".
	Scope            string
	Races            []RaceRow
	ChartLabels      []string
	ChartData        []int64
	Clans            []ClanRow
	TribeLabels      []string
	TribeData        []int64
	TribeRaces       []string
	Genders          []ShareRow
	GenderLabels     []string
	GenderData       []int64
	RaceGenders      []RaceGenderRow
	RaceGenderLabels []string
	FemaleShare      string
	HasUnknownGender bool
	Filters          FilterBar
}

// Races handles GET /ui/races.
func (c *UIController) Races(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	selectedRegion, selectedDC, selectedWorld := place.Region, place.Datacenter, place.World

	scope := place.scope()
	scopeText := placeScope(selectedRegion, selectedDC, selectedWorld)

	newWindow := census.NewCharactersWindow(snapshot, scope)

	data := RacesViewData{
		NewCharacters:      newWindow.Current,
		NewCharactersTrend: newTrend(newWindow),
		SelectedRegion:     selectedRegion,
		SelectedDC:         selectedDC,
		SelectedWorld:      selectedWorld,
		Scope:              scopeText,
	}

	races := census.SnapshotGroups(snapshot, "race", scope)
	for _, row := range races {
		data.TotalCharacters += row.Total
		data.ActiveCharacters += row.Active
	}
	data.ActivePercent = formatPercent(data.ActiveCharacters, data.TotalCharacters)
	data.Races = newRaceRows(races, data.TotalCharacters)
	for _, row := range data.Races {
		data.ChartLabels = append(data.ChartLabels, row.Race)
		data.ChartData = append(data.ChartData, row.Total)
	}

	data.Clans = clanRows(census.SnapshotGroups(snapshot, "tribe", scope), data.TotalCharacters)
	for _, clan := range data.Clans {
		data.TribeLabels = append(data.TribeLabels, clan.Clan)
		data.TribeData = append(data.TribeData, clan.Total)
		data.TribeRaces = append(data.TribeRaces, clan.Race)
	}

	var genderTotal int64
	genders := census.SnapshotGroups(snapshot, "gender", scope)
	for _, row := range genders {
		genderTotal += row.Total
	}
	for _, row := range genders {
		data.Genders = append(data.Genders, ShareRow{Name: row.Key, Total: row.Total, Share: formatPercent(row.Total, genderTotal)})
		data.GenderLabels = append(data.GenderLabels, row.Key)
		data.GenderData = append(data.GenderData, row.Total)
	}

	data.RaceGenders = raceGenderRows(census.SnapshotGroups(snapshot, "race_gender", scope))
	var female, all int64
	for _, row := range data.RaceGenders {
		data.RaceGenderLabels = append(data.RaceGenderLabels, row.Race)
		female += row.Female
		data.HasUnknownGender = data.HasUnknownGender || row.Unknown > 0
		all += row.Male + row.Female + row.Unknown
	}
	data.FemaleShare = formatPercent(female, all)

	title := "Race & Clan Demographics"
	if selectedWorld != "" {
		title = fmt.Sprintf("Race & Clan Demographics - %s", selectedWorld)
	} else if selectedDC != "" {
		title = fmt.Sprintf("Race & Clan Demographics - %s DC", selectedDC)
	} else if selectedRegion != "" {
		title = fmt.Sprintf("Race & Clan Demographics - %s", selectedRegion)
	}

	data.Filters = placeFilterBar("/ui/races", "Filter races", place)

	c.render(w, "templates/races.html", statsPageData(title, "races", "/ui/races", "Race and clan demographics for Final Fantasy XIV: population shares, active ratios, and tribe and gender breakdowns filterable by region, datacenter, and world.", state, data))
}

// clanRows groups clans under their race (fixed race order, largest clan
// first within a race); clans of no known race come last.
func clanRows(groups []contract.GroupCount, total int64) []ClanRow {
	var rows []ClanRow
	for _, g := range groups {
		if g.Key == "" {
			continue
		}
		rows = append(rows, ClanRow{Clan: g.Key, Race: census.ClanRace(g.Key), Total: g.Total, Share: formatPercent(g.Total, total)})
	}
	rank := func(race string) int {
		if s := raceSlot(race); s > 0 {
			return s
		}
		return len(census.RaceOrder()) + 1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := rank(rows[i].Race), rank(rows[j].Race); ri != rj {
			return ri < rj
		}
		return rows[i].Total > rows[j].Total
	})
	return rows
}

// raceGenderRows pivots "Race|Gender" groups into one row per race, in the
// fixed race order (unknown races last, alphabetically).
func raceGenderRows(groups []contract.GroupCount) []RaceGenderRow {
	byRace := map[string]*RaceGenderRow{}
	for _, g := range groups {
		race, gender, ok := strings.Cut(g.Key, "|")
		if !ok || race == "" {
			continue
		}
		row := byRace[race]
		if row == nil {
			row = &RaceGenderRow{Race: race}
			byRace[race] = row
		}
		switch gender {
		case "Male":
			row.Male += g.Total
		case "Female":
			row.Female += g.Total
		default:
			row.Unknown += g.Total
		}
	}
	var rows []RaceGenderRow
	for _, race := range census.RaceOrder() {
		if row, ok := byRace[race]; ok {
			rows = append(rows, *row)
			delete(byRace, race)
		}
	}
	var rest []string
	for race := range byRace {
		rest = append(rest, race)
	}
	sort.Strings(rest)
	for _, race := range rest {
		rows = append(rows, *byRace[race])
	}
	return rows
}
