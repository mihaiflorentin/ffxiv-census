package ui

import (
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
	NewCharacters30d   int64
	NewCharactersTrend Trend
	SelectedRegion     string
	SelectedDC         string
	// Scope reads like "across every region" or "in Europe".
	Scope   string
	Worlds  []WorldDetailRow
	Filters FilterBar
}

// WorldDetailRow represents one row in the global worlds table.
type WorldDetailRow struct {
	World            string
	Datacenter       string
	Region           string
	Total            int64
	Active           int64
	ActiveRatio      string
	NewCharacters30d int64
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
			World:            wName,
			Datacenter:       wDC,
			Region:           wReg,
			Total:            row.Total,
			Active:           row.Active,
			ActiveRatio:      formatPercent(row.Active, row.Total),
			NewCharacters30d: census.NewCharactersWindow(snapshot, contract.StatsScope{World: wName}).Current,
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

	sort.Slice(filteredWorlds, func(i, j int) bool {
		return filteredWorlds[i].Total > filteredWorlds[j].Total
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

	newWindow := census.NewCharactersWindow(snapshot, contract.StatsScope{Region: selectedRegion, Datacenter: selectedDC})

	c.render(w, "templates/worlds.html", statsPageData("Worlds Census", "worlds", "/ui/worlds", "Final Fantasy XIV world rankings: total and active character counts for every world and datacenter, with region filters and 30-day activity ratios.", state, WorldsViewData{
		TotalCharacters:    totalChars,
		ActiveCharacters:   activeChars,
		NewCharacters30d:   newWindow.Current,
		NewCharactersTrend: newTrend(newWindow.Current, newWindow.Previous),
		Scope:              placeScope(selectedRegion, selectedDC, ""),
		SelectedRegion:     selectedRegion,
		SelectedDC:         selectedDC,
		Worlds:             filteredWorlds,
		Filters:            worldsFilterBar(regionList, dcList, selectedRegion, selectedDC),
	}))
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
