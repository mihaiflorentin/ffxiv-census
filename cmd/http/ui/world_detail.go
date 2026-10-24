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
	World      string
	Datacenter string
	// Region is the region code (NA, EU, JP, OCE).
	Region             string
	TotalCharacters    int64
	ActiveCharacters   int64
	ActiveRatio        string
	NewCharacters30d   int64
	NewCharactersTrend Trend
	// Races are ranked by characters; RaceLabels/RaceData feed the chart.
	Races          []RaceRow
	RaceLabels     []string
	RaceData       []int64
	MSQCompletions []ExpansionCard
	Timeline       Timeline
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
	window := census.NewCharactersWindow(snapshot, scope)
	dc := WorldToDC(worldName)
	region := census.RegionForDatacenter(dc)

	var raceRows []RaceRow
	for _, row := range census.SnapshotGroups(snapshot, "race", scope) {
		name := row.Key
		if name == "" {
			name = "Unknown"
		}
		var share float64
		if total > 0 {
			share = float64(row.Total) / float64(total) * 100
		}
		raceRows = append(raceRows, RaceRow{
			Race:            name,
			Slot:            raceSlot(name),
			Total:           row.Total,
			Active:          row.Active,
			ActiveRatio:     formatPercent(row.Active, row.Total),
			ShareOfTotal:    formatPercent(row.Total, total),
			SharePercentVal: share,
		})
	}
	sort.SliceStable(raceRows, func(i, j int) bool { return raceRows[i].Total > raceRows[j].Total })
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
		percent := formatPercent(count, total)
		msq = append(msq, ExpansionCard{Name: exp.Name, Count: count, Percent: percent, Value: strings.TrimSuffix(percent, "%")})
	}

	c.render(w, "templates/world_detail.html", statsPageData(fmt.Sprintf("%s - World Demographics", worldName), "worlds", fmt.Sprintf("/ui/worlds/%s", worldName), fmt.Sprintf("Character population for the FFXIV world %s: active players, race demographics, MSQ completions, and new characters over the last 30 days.", worldName), state, WorldDetailViewData{
		World:              worldName,
		Datacenter:         dc,
		Region:             region,
		TotalCharacters:    total,
		ActiveCharacters:   active,
		ActiveRatio:        formatPercent(active, total),
		NewCharacters30d:   window.Current,
		NewCharactersTrend: newTrend(window.Current, window.Previous),
		Races:              raceRows,
		RaceLabels:         raceLabels,
		RaceData:           raceData,
		MSQCompletions:     msq,
		Timeline:           newCharactersTimeline(snapshot, scope),
	}))
}
