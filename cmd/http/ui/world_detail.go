package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// WorldDetailViewData holds statistics and chart payloads for a single world view.
type WorldDetailViewData struct {
	World                 string
	Datacenter            string
	Region                string
	TotalCharacters       int64
	ActiveCharacters      int64
	ActiveRatio           string
	ActivePercentVal      float64
	NewCharacters30d      int64
	NewCharactersTrend    string
	Races                 []RaceRow
	MSQCompletions        []MSQRow
	NewCharactersTimeline []DailyRow
	RaceChartLabels       []string
	RaceChartData         []int64
	MSQChartLabels        []string
	MSQChartData          []int64
	TimelineChartLabels   []string
	TimelineChartData     []int64
}

// MSQRow represents expansion completion statistics.
type MSQRow struct {
	Expansion    string
	Count        int64
	PercentTotal string
}

// DailyRow represents daily counts for timelines.
type DailyRow struct {
	Day   string
	Count int64
}

// WorldDetail handles GET /ui/worlds/{world}.
func (c *UIController) WorldDetail(w http.ResponseWriter, r *http.Request) {
	worldName := strings.TrimSpace(r.PathValue("world"))
	if worldName == "" {
		http.Redirect(w, r, "/ui/worlds", http.StatusFound)
		return
	}
	if !isIndexableWorld(worldName) {
		c.renderNotFound(w, r, NotFoundViewData{
			Heading: "World not found",
			Message: fmt.Sprintf("There is no world named %s in the census. Check the spelling, or pick a world from the full list.", worldName),
		})
		return
	}

	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}

	var total, active int64
	for _, world := range census.SnapshotGroups(snapshot, "world", contract.StatsScope{}) {
		if strings.EqualFold(world.Key, worldName) {
			total, active = world.Total, world.Active
			break
		}
	}
	scope := contract.StatsScope{World: worldName}
	timeline := census.SnapshotDaily(snapshot, scope)
	window := census.NewCharactersWindow(snapshot, scope)
	var windowDays []contract.DailyCount
	if snapshot != nil {
		for _, day := range timeline {
			if day.Day >= window.StartDay {
				windowDays = append(windowDays, day)
			}
		}
	}
	dcName := WorldToDC(worldName)
	stats := &census.WorldDetailStats{
		World:                 worldName,
		Datacenter:            dcName,
		Region:                census.RegionForDatacenter(dcName),
		TotalCharacters:       total,
		ActiveCharacters:      active,
		NewCharacters30d:      window.Current,
		Races:                 census.SnapshotGroups(snapshot, "race", scope),
		MSQCompletions:        census.SnapshotExpansions(snapshot, scope),
		NewCharactersTimeline: windowDays,
	}

	dc := stats.Datacenter
	if dc == "" {
		dc = WorldToDC(worldName)
	}
	region := stats.Region
	if region == "" {
		region = census.RegionForDatacenter(dc)
	}
	if region == "" {
		region = "Unknown"
	}

	var activePctVal float64
	if stats.TotalCharacters > 0 {
		activePctVal = (float64(stats.ActiveCharacters) / float64(stats.TotalCharacters)) * 100
	}

	var raceRows []RaceRow
	var raceLabels []string
	var raceData []int64
	for _, row := range stats.Races {
		rName := row.Key
		if rName == "" {
			rName = "Unknown"
		}
		var sharePctVal float64
		if stats.TotalCharacters > 0 {
			sharePctVal = (float64(row.Total) / float64(stats.TotalCharacters)) * 100
		}
		raceRows = append(raceRows, RaceRow{
			Race:            rName,
			Total:           row.Total,
			Active:          row.Active,
			ActiveRatio:     formatPercent(row.Active, row.Total),
			ShareOfTotal:    formatPercent(row.Total, stats.TotalCharacters),
			SharePercentVal: sharePctVal,
		})
		raceLabels = append(raceLabels, rName)
		raceData = append(raceData, row.Total)
	}
	sort.Slice(raceRows, func(i, j int) bool {
		return raceRows[i].Total > raceRows[j].Total
	})

	msqMap := make(map[string]int64)
	for _, row := range stats.MSQCompletions {
		msqMap[row.Expansion] = row.Count
	}

	var expansionList []census.ExpansionConfig
	if c.svc != nil {
		expansionList = c.svc.Expansions()
	}
	if len(expansionList) == 0 {
		expansionList = census.DefaultExpansions
	}

	var msqRows []MSQRow
	var msqLabels []string
	var msqData []int64
	for _, exp := range expansionList {
		count := msqMap[exp.Name]
		pct := "0.0%"
		if stats.TotalCharacters > 0 {
			pct = formatPercent(count, stats.TotalCharacters)
		}
		msqRows = append(msqRows, MSQRow{
			Expansion:    exp.Name,
			Count:        count,
			PercentTotal: pct,
		})
		msqLabels = append(msqLabels, exp.Name)
		msqData = append(msqData, count)
	}
	var timelineRows []DailyRow
	var timelineLabels []string
	var timelineData []int64
	for _, row := range stats.NewCharactersTimeline {
		timelineRows = append(timelineRows, DailyRow{
			Day:   row.Day,
			Count: row.Count,
		})
		timelineLabels = append(timelineLabels, row.Day)
		timelineData = append(timelineData, row.Count)
	}

	c.render(w, "templates/world_detail.html", statsPageData(fmt.Sprintf("%s - World Demographics", worldName), "worlds", fmt.Sprintf("/ui/worlds/%s", worldName), fmt.Sprintf("Character population for the FFXIV world %s: active players, race demographics, MSQ completions, and new characters over the last 30 days.", worldName), state, WorldDetailViewData{
		World:                 worldName,
		Datacenter:            dc,
		Region:                region,
		TotalCharacters:       stats.TotalCharacters,
		ActiveCharacters:      stats.ActiveCharacters,
		ActiveRatio:           formatPercent(stats.ActiveCharacters, stats.TotalCharacters),
		ActivePercentVal:      activePctVal,
		NewCharacters30d:      stats.NewCharacters30d,
		NewCharactersTrend:    formatTrend(window.Current, window.Previous),
		Races:                 raceRows,
		MSQCompletions:        msqRows,
		NewCharactersTimeline: timelineRows,
		RaceChartLabels:       raceLabels,
		RaceChartData:         raceData,
		MSQChartLabels:        msqLabels,
		MSQChartData:          msqData,
		TimelineChartLabels:   timelineLabels,
		TimelineChartData:     timelineData,
	}))
}
