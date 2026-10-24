package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// WorldsViewData holds world demographics, filter state, and lists for /ui/worlds.
type WorldsViewData struct {
	TotalCharacters    int64
	ActiveCharacters   int64
	NewCharacters      int64
	NewCharactersTrend Trend
	SelectedRegion     string
	SelectedDC         string
	// Scope reads like "across every region" or "in Europe".
	Scope   string
	Worlds  []WorldDetailRow
	Filters FilterBar
	// Rank is the measure the rankings are ordered by ("characters" or
	// "active"); RankSwitch flips between the two.
	Rank       string
	RankSwitch Switch
}

const (
	worldsRankCharacters = "characters"
	worldsRankActive     = "active"
	// worldRankingsRegion is the id of the region the rank switch swaps.
	worldRankingsRegion = "world-rankings"
)

// WorldDetailRow represents one row in the global worlds table.
type WorldDetailRow struct {
	World         string
	Datacenter    string
	Region        string
	Total         int64
	Active        int64
	ActiveRatio   string
	NewCharacters int64
}

// Worlds handles GET /ui/worlds.
func (c *UIController) Worlds(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	selectedRegion, selectedDC := place.Region, place.Datacenter

	var allWorlds []WorldDetailRow
	var totalChars, activeChars int64

	regionSet := make(map[string]bool)
	dcSet := make(map[string]bool)

	for _, row := range census.SnapshotGroups(snapshot, "world", contract.StatsScope{}) {
		wName := row.Key
		if !isIndexableWorld(wName) {
			continue // skip unassigned worlds and worlds outside the known hierarchy
		}
		wDC := WorldToDC(wName)
		wReg := census.RegionForDatacenter(wDC)

		regionSet[wReg] = true
		dcSet[wDC] = true

		allWorlds = append(allWorlds, WorldDetailRow{
			World:         wName,
			Datacenter:    wDC,
			Region:        wReg,
			Total:         row.Total,
			Active:        row.Active,
			ActiveRatio:   formatPercent(row.Active, row.Total),
			NewCharacters: c.memo.WorldNewCharacters(snapshot, wName).Current,
		})
	}

	// Filter
	var filteredWorlds []WorldDetailRow
	for _, wRow := range allWorlds {
		if selectedRegion != "" && !strings.EqualFold(wRow.Region, selectedRegion) {
			continue
		}
		if selectedDC != "" && !strings.EqualFold(wRow.Datacenter, selectedDC) {
			continue
		}
		filteredWorlds = append(filteredWorlds, wRow)
		totalChars += wRow.Total
		activeChars += wRow.Active
	}

	rank := worldsRankCharacters
	if strings.EqualFold(r.URL.Query().Get("sort"), worldsRankActive) {
		rank = worldsRankActive
	}
	sort.SliceStable(filteredWorlds, func(i, j int) bool {
		a, b := filteredWorlds[i], filteredWorlds[j]
		if rank == worldsRankActive && a.Active != b.Active {
			return a.Active > b.Active
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.World < b.World
	})

	var regionList []string
	for reg := range regionSet {
		regionList = append(regionList, reg)
	}
	sortRegionCodes(regionList)

	var dcList []string
	if selectedRegion != "" {
		dcList = DCsForRegion(selectedRegion)
	} else {
		for dc := range dcSet {
			dcList = append(dcList, dc)
		}
		sort.Strings(dcList)
	}

	newWindow := census.NewCharactersTotals(snapshot, contract.StatsScope{Region: selectedRegion, Datacenter: selectedDC})

	data := statsPageData("Worlds Census", "worlds", "/ui/worlds", fmt.Sprintf("Final Fantasy XIV world rankings: total characters and players active in the last %s for every world and datacenter, with region filters.", Window{Days: state.ActivityWindowDays}.Long()), state, WorldsViewData{
		TotalCharacters:    totalChars,
		ActiveCharacters:   activeChars,
		NewCharacters:      newWindow.Current,
		NewCharactersTrend: newTrend(newWindow),
		Scope:              placeScope(selectedRegion, selectedDC, ""),
		SelectedRegion:     selectedRegion,
		SelectedDC:         selectedDC,
		Worlds:             filteredWorlds,
		Filters:            worldsFilterBar(regionList, dcList, selectedRegion, selectedDC),
		Rank:               rank,
		RankSwitch:         worldsRankSwitch(selectedRegion, selectedDC, rank),
	})
	if wantsSwitchRegion(r, worldRankingsRegion) {
		c.renderRegion(w, "templates/worlds.html", "world-rankings", data)
		return
	}
	c.render(w, "templates/worlds.html", data)
}

// worldsRankSwitch builds the "Characters / Active players" switch for the
// world rankings. Both links keep the region and datacenter filters.
func worldsRankSwitch(region, dc, rank string) Switch {
	href := func(sortBy string) string {
		q := url.Values{}
		if region != "" {
			q.Set("region", region)
		}
		if dc != "" {
			q.Set("dc", dc)
		}
		if sortBy != worldsRankCharacters {
			q.Set("sort", sortBy)
		}
		if len(q) == 0 {
			return "/ui/worlds"
		}
		return "/ui/worlds?" + q.Encode()
	}
	return Switch{
		Label:  "Rank worlds by",
		Target: worldRankingsRegion,
		Options: []SwitchOption{
			{Label: "Characters", Href: href(worldsRankCharacters), Current: rank == worldsRankCharacters},
			{Label: "Active players", Href: href(worldsRankActive), Current: rank == worldsRankActive},
		},
	}
}

// worldsFilterBar builds the Worlds filter bar: region pills (links that drop
// any datacenter) and, when there are datacenters to pick, a datacenter select
// that keeps the selected region.
func worldsFilterBar(regions, dcs []string, selectedRegion, selectedDC string) FilterBar {
	pills := &FilterPills{ID: "filter-region-label", Label: "Region", Options: []FilterLink{
		{Label: "All", Href: "/ui/worlds", Current: selectedRegion == "" && selectedDC == ""},
	}}
	for _, reg := range regions {
		pills.Options = append(pills.Options, FilterLink{
			Label:   regionDisplayName(reg),
			Href:    "/ui/worlds?region=" + url.QueryEscape(reg),
			Current: selectedRegion == reg,
		})
	}
	bar := FilterBar{Action: "/ui/worlds", Label: "Filter worlds", Pills: pills, ClearHref: "/ui/worlds",
		Chips: filterChips("/ui/worlds", []appliedFilter{
			{Name: "region", Label: "Region", Value: selectedRegion, Shown: regionDisplayName(selectedRegion)},
			{Name: "dc", Label: "Datacenter", Value: selectedDC},
		}),
	}
	if len(dcs) > 0 {
		if selectedRegion != "" {
			bar.Hidden = []FilterHidden{{Name: "region", Value: selectedRegion}}
		}
		bar.Selects = []FilterSelect{{ID: "filter-dc", Name: "dc", Label: "Datacenter", AllLabel: "All datacenters", Options: filterOptions(dcs, selectedDC, nil)}}
	}
	return bar
}
