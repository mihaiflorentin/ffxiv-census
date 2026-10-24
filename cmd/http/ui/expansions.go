package ui

import (
	"net/http"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ExpansionsViewData holds MSQ completion funnel metrics for /ui/expansions.
type ExpansionsViewData struct {
	TotalCharacters int64
	Expansions      []ExpansionProgression
}

// ExpansionProgression is one step of the main scenario progression.
type ExpansionProgression struct {
	Name        string
	Version     string
	FinalQuest  string
	LevelCap    uint32
	Completions int64
	// PercentOfTotal is the share of all characters ("12.5%"); MeterValue
	// the same share as a plain 0-100 number for the step's meter.
	PercentOfTotal string
	MeterValue     float64
	// Previous names the expansion before this one ("" for the first).
	Previous string
	// Retention is the share of Previous's finishers who also finished this
	// one, or "Not measured" when nobody finished Previous.
	Retention string
}

// Expansions handles GET /ui/expansions.
func (c *UIController) Expansions(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	totalChars := snapshot.Summary.Total
	completions := census.SnapshotExpansions(snapshot, contract.StatsScope{})
	countMap := make(map[string]int64, len(completions))
	for _, completion := range completions {
		countMap[completion.Expansion] = completion.Count
	}

	var expansionList []census.ExpansionConfig
	if c.svc != nil {
		expansionList = c.svc.Expansions()
	}
	if len(expansionList) == 0 {
		expansionList = census.DefaultExpansions
	}

	var list []ExpansionProgression
	for i, info := range expansionList {
		count := countMap[info.Name]
		percent := formatPercent(count, totalChars)
		step := ExpansionProgression{
			Name:           info.Name,
			Version:        info.Version,
			FinalQuest:     info.FinalQuest,
			LevelCap:       info.LevelCap,
			Completions:    count,
			PercentOfTotal: percent,
			MeterValue:     percentValue(count, totalChars),
		}
		if i > 0 {
			prev := expansionList[i-1]
			step.Previous = prev.Name
			step.Retention = "Not measured"
			if prevCount := countMap[prev.Name]; prevCount > 0 {
				step.Retention = formatPercent(count, prevCount)
			}
		}
		list = append(list, step)
	}

	c.render(w, "templates/expansions.html", statsPageData("Expansion Progression", "expansions", "/ui/expansions", "Main scenario quest completion rates for every Final Fantasy XIV expansion from A Realm Reborn to Dawntrail, with retention and drop-off metrics.", state, ExpansionsViewData{
		TotalCharacters: totalChars,
		Expansions:      list,
	}))
}
