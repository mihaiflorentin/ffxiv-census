package ui

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// WorldDetailViewData holds statistics and chart payloads for a single world view.
type WorldDetailViewData struct {
	World      string
	Datacenter string
	// Region is the region code (NA, EU, JP, OCE).
	Region             string
	TotalCharacters    int64
	ActiveCharacters   int64
	ActiveRatio        string
	NewCharacters      int64
	NewCharactersTrend Trend
	// Races are ranked by characters; RaceLabels/RaceData feed the chart.
	Races          []RaceRow
	RaceLabels     []string
	RaceData       []int64
	MSQCompletions []ExpansionCard
	GrandCompanies GrandCompanyBand
	TopJobs        TopJobsBlock
	FreeCompanies  TopFreeCompaniesBlock
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
	window := census.NewCharactersTotals(snapshot, scope)
	dc := WorldToDC(worldName)
	region := census.RegionForDatacenter(dc)

	// SnapshotGroups ranks the races by characters already.
	raceRows := newRaceRows(census.SnapshotGroups(snapshot, "race", scope), total)
	var raceLabels []string
	var raceData []int64
	for _, row := range raceRows {
		raceLabels = append(raceLabels, row.Race)
		raceData = append(raceData, row.Total)
	}

	completions := make(map[string]int64)
	for _, row := range census.SnapshotExpansions(snapshot, scope) {
		completions[row.Expansion] = row.Count
	}
	var expansionList []census.ExpansionConfig
	if c.svc != nil {
		expansionList = c.svc.Expansions()
	}
	if len(expansionList) == 0 {
		expansionList = census.DefaultExpansions
	}
	var msq []ExpansionCard
	for _, exp := range expansionList {
		count := completions[exp.Name]
		msq = append(msq, ExpansionCard{Name: exp.Name, Count: count, Percent: formatPercent(count, total), Value: percentValue(count, total)})
	}

	fcBoard, fcErr := c.stats.FreeCompanyBoard(r.Context(), snapshot,
		census.FreeCompanyQuery{Measure: census.FreeCompanyMembers, Scope: scope})

	c.render(w, "templates/world_detail.html", statsPageData(fmt.Sprintf("%s - World Demographics", worldName), "worlds", fmt.Sprintf("/ui/worlds/%s", worldName), fmt.Sprintf("Character population for the FFXIV world %s: players active in the last %s, new characters in the last %s, race demographics and MSQ completions.", worldName, Window{Days: state.ActivityWindowDays}.Long(), Window{Days: state.NewCharactersWindowDays}.Long()), state, WorldDetailViewData{
		World:              worldName,
		Datacenter:         dc,
		Region:             region,
		TotalCharacters:    total,
		ActiveCharacters:   active,
		ActiveRatio:        formatPercent(active, total),
		NewCharacters:      window.Current,
		NewCharactersTrend: newTrend(window),
		Races:              raceRows,
		RaceLabels:         raceLabels,
		RaceData:           raceData,
		MSQCompletions:     msq,
		GrandCompanies: grandCompanyBand(c.memo.GrandCompanySplit(snapshot, scope),
			census.SnapshotHasSection(snapshot, census.SectionGrandCompanies), worldName),
		TopJobs: topJobsBlock(c.memo.Armoury(snapshot, scope),
			census.SnapshotHasSection(snapshot, census.SectionJobs), worldName),
		FreeCompanies: topFreeCompaniesBlock(worldName, census.SnapshotLocation(snapshot, scope), fcBoard, fcErr),
	}))
}
