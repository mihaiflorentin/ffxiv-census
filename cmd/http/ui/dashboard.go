package ui

import (
	"net/http"
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// DashboardViewData holds the aggregate metrics and time-series for the dashboard.
type DashboardViewData struct {
	TotalCharacters      int64
	ActiveCharacters     int64
	ActivePercent        string
	MaxLevelCharacters   int64
	MaxLevelPercent      string
	MaxLevel             uint32
	NewCharacters30d     int64
	NewCharactersTrend   Trend
	ChartLabels          []string
	ChartDates           []string
	ChartData            []int64
	Timeline             TimelineSummary
	Regions              []RegionSummary
	RaceLabels           []string
	RaceData             []int64
	Races                []ShareRow
	ExpansionCompletions []ExpansionCard
	RealmTally           RealmTally
}

// ShareRow is one category's count and share of the census, used as the
// text alternative of part-to-whole charts.
type ShareRow struct {
	Name  string
	Total int64
	Share string
}

// TimelineSummary feeds the one-sentence text alternative of the 30-day chart.
type TimelineSummary struct {
	Total     int64
	PeakDate  string
	PeakCount int64
}

// ExpansionCard holds completion stats for one expansion.
type ExpansionCard struct {
	Name    string
	Count   int64
	Percent string
	// Value is the completion share as a plain number (0-100) for the meter.
	Value string
}

// RegionSummary holds aggregated stats for a region in the dashboard.
type RegionSummary struct {
	Region      string
	Name        string
	Total       int64
	Active      int64
	ActiveRatio string
}

// WorldDrilldownData holds the filtered world rows for the HTMX partial.
type WorldDrilldownData struct {
	Region string
	Worlds []WorldRow
}

// WorldRow holds stats for an individual world.
type WorldRow struct {
	Region           string
	Datacenter       string
	World            string
	Total            int64
	Active           int64
	NewCharacters30d int64
	ActiveRatio      string
}

// Dashboard handles GET /ui/dashboard.
func (c *UIController) Dashboard(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	total := snapshot.Summary.Total
	active := snapshot.Summary.Active
	maxLevelCount := snapshot.Summary.MaxLevel
	maxLevel := snapshot.MaxLevel
	if maxLevel == 0 {
		maxLevel = 100
	}

	var regions []RegionSummary
	regionGroups := census.SnapshotGroups(snapshot, "region", contract.StatsScope{})
	for _, row := range regionGroups {
		name := row.Key
		if name == "" {
			name = "Unknown"
		}
		regions = append(regions, RegionSummary{Region: name, Name: regionDisplayName(name), Total: row.Total, Active: row.Active, ActiveRatio: formatPercent(row.Active, row.Total)})
	}
	var raceLabels []string
	var raceData []int64
	var raceRows []ShareRow
	for _, row := range census.SnapshotGroups(snapshot, "race", contract.StatsScope{}) {
		if row.Key != "" {
			raceLabels = append(raceLabels, row.Key)
			raceData = append(raceData, row.Total)
			raceRows = append(raceRows, ShareRow{Name: row.Key, Total: row.Total, Share: formatPercent(row.Total, total)})
		}
	}

	completions := census.SnapshotExpansions(snapshot, contract.StatsScope{})
	countMap := make(map[string]int64, len(completions))
	for _, completion := range completions {
		countMap[completion.Expansion] = completion.Count
	}
	expansions := census.DefaultExpansions
	if c.svc != nil && len(c.svc.Expansions()) > 0 {
		expansions = c.svc.Expansions()
	}
	var expansionCards []ExpansionCard
	for _, expansion := range expansions {
		expansionCards = append(expansionCards, ExpansionCard{Name: expansion.Name, Count: countMap[expansion.Name]})
	}

	timeline := newCharactersTimeline(snapshot, contract.StatsScope{})

	// Fix expansion percents now that total is known
	for i := range expansionCards {
		expansionCards[i].Percent = formatPercent(expansionCards[i].Count, total)
		expansionCards[i].Value = strings.TrimSuffix(expansionCards[i].Percent, "%")
	}

	newCharactersWindow := census.NewCharactersWindow(snapshot, contract.StatsScope{})

	viewData := DashboardViewData{
		TotalCharacters:      total,
		ActiveCharacters:     active,
		ActivePercent:        formatPercent(active, total),
		NewCharacters30d:     newCharactersWindow.Current,
		NewCharactersTrend:   newTrend(newCharactersWindow.Current, newCharactersWindow.Previous),
		MaxLevelCharacters:   maxLevelCount,
		MaxLevelPercent:      formatPercent(maxLevelCount, total),
		MaxLevel:             maxLevel,
		ChartLabels:          timeline.Labels,
		ChartDates:           timeline.Dates,
		ChartData:            timeline.Counts,
		Timeline:             timeline.TimelineSummary,
		Regions:              regions,
		RaceLabels:           raceLabels,
		RaceData:             raceData,
		Races:                raceRows,
		ExpansionCompletions: expansionCards,
		RealmTally:           newRealmTally(total, regionGroups),
	}

	c.render(w, "templates/dashboard.html", statsPageData("Dashboard", "dashboard", "/", "Final Fantasy XIV population census: total characters, active players, race demographics, and 30-day growth trends across every region and datacenter.", state, viewData))
}

// WorldDrilldown handles GET /ui/partials/world-breakdown?region=NA.
func (c *UIController) WorldDrilldown(w http.ResponseWriter, r *http.Request) {
	snapshot, _, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	region := parsePlaceFilter(r).Region
	if region == "" {
		region = "NA"
	}

	var worlds []WorldRow
	for _, wc := range census.SnapshotGroups(snapshot, "world", contract.StatsScope{}) {
		wName := wc.Key
		wDC := WorldToDC(wName)
		wReg := census.RegionForDatacenter(wDC)
		if wReg == "" {
			wReg = "Unknown"
		}

		if strings.EqualFold(wReg, region) || (region == "Unknown" && wReg == "Unknown") {
			newCharacters := census.NewCharactersWindow(snapshot, contract.StatsScope{World: wName})
			worlds = append(worlds, WorldRow{
				Region:           wReg,
				Datacenter:       wDC,
				World:            wName,
				Total:            wc.Total,
				Active:           wc.Active,
				NewCharacters30d: newCharacters.Current,
				ActiveRatio:      formatPercent(wc.Active, wc.Total),
			})
		}
	}

	sort.Slice(worlds, func(i, j int) bool {
		if worlds[i].Datacenter != worlds[j].Datacenter {
			return worlds[i].Datacenter < worlds[j].Datacenter
		}
		return worlds[i].Total > worlds[j].Total
	})

	c.renderPartial(w, "templates/partials/world_drilldown.html", WorldDrilldownData{
		Region: region,
		Worlds: worlds,
	})
}
