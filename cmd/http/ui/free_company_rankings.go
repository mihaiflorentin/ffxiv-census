package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
)

// fcBoardRegion is the id of the board the measure switch swaps.
const fcBoardRegion = "fc-board"

// fcRankingsPath is the Free Company boards' page.
const fcRankingsPath = "/ui/rankings/free-companies"

// worldTopFreeCompanies is how many Free Companies a world page shows.
const worldTopFreeCompanies = 5

// FreeCompanyRankingsViewData feeds /ui/rankings/free-companies.
type FreeCompanyRankingsViewData struct {
	Tabs []RankingsTab
	// Title heads the board; Scope reads "Across every world" or
	// "On Balmung"; Announce is what a screen reader hears after a switch.
	Title, Scope, Announce string
	// Active is true on the active-members board.
	Active  bool
	Switch  Switch
	Filters FilterBar
	// Ready is false while there are no boards (a snapshot from before
	// them, or a missing row).
	Ready    bool
	Filtered bool
	Ladder   Ladder
	BuiltAt  time.Time
	// Membership reads "40.0% of characters on Balmung belong to a Free
	// Company."; empty without characters.
	Membership string
}

// FreeCompanyRankings handles GET /ui/rankings/free-companies: the top Free
// Companies of a place, by members seen or by active members.
func (c *UIController) FreeCompanyRankings(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	measure := census.FreeCompanyMembers
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("measure")), string(census.FreeCompanyActive)) {
		measure = census.FreeCompanyActive
	}
	active := measure == census.FreeCompanyActive
	title := "Largest Free Companies"
	if active {
		title = "Most active Free Companies"
	}
	data := FreeCompanyRankingsViewData{
		Tabs:     rankingsTabs("free-companies"),
		Title:    title,
		Scope:    fcScopeLine(place),
		Announce: title + ", " + fcAnnounceScope(place),
		Active:   active,
		Switch:   fcMeasureSwitch(place, measure),
		Filters:  fcFilterBar(place, measure),
		Filtered: place != (placeFilter{}),
	}
	result, err := c.stats.FreeCompanyBoard(r.Context(), snapshot, census.FreeCompanyQuery{Measure: measure, Scope: place.scope()})
	if err != nil {
		logging.Error("ui.free_companies", err.Error())
	}
	data.Ready = err == nil && result.Ready
	if data.Ready {
		data.Membership = fcMembershipLine(census.SnapshotLocation(snapshot, place.scope()), fcPlacePhrase(place))
		data.BuiltAt = result.BuiltAt
		data.Ladder = fcLadder(result.Rows, measure)
		data.Ladder.Caption = data.Announce
		data.Ladder.ValueHeading = "Members seen"
		data.Ladder.DetailHeading = "Active members"
		if active {
			data.Ladder.ValueHeading, data.Ladder.DetailHeading = "Active members", "Members seen"
		}
		data.Ladder.Entrance = true
	}

	page := statsPageData(fcPageTitle(place), "rankings", fcRankingsPath,
		"The 100 largest and the 100 most active Final Fantasy XIV Free Companies of every world, datacenter and region, from the members the census has seen.",
		state, data)
	if wantsSwitchRegion(r, fcBoardRegion) {
		c.renderRegion(w, "templates/free_company_rankings.html", fcBoardRegion, page)
		return
	}
	c.render(w, "templates/free_company_rankings.html", page)
}

// fcLadder turns board rows into ladder rungs: the board's measure is the
// large figure and the other measure the detail line.
func fcLadder(rows []census.FreeCompanyRow, measure census.FreeCompanyMeasure) Ladder {
	rungs := make([]LadderRung, 0, len(rows))
	for _, row := range rows {
		value, unit, detail := formatNumber(row.Members), "members", fmt.Sprintf("%s active", formatNumber(row.Active))
		if measure == census.FreeCompanyActive {
			value, unit, detail = formatNumber(row.Active), "active", fmt.Sprintf("%s members", formatNumber(row.Members))
		}
		rungs = append(rungs, LadderRung{
			Rank: row.Rank, Name: row.Name,
			Note: row.World, NoteHref: "/ui/worlds/" + url.PathEscape(row.World),
			Value: value, Unit: unit, Detail: detail,
			Bar: barWidth(row.ShareOfLeader), Leader: row.Rank == 1,
			ExternalHref:  lodestoneFreeCompanyURL(row.ID),
			ExternalLabel: "Lodestone",
		})
	}
	l := newLadder(rungs, "Free Companies")
	l.NameHeading, l.NoteHeading = "Free Company", "World"
	return l
}

// lodestoneFreeCompanyURL is a Free Company's official Lodestone page.
func lodestoneFreeCompanyURL(id string) string {
	return "https://na.finalfantasyxiv.com/lodestone/freecompany/" + url.PathEscape(id) + "/"
}

// fcHref is the canonical link to a board: the default measure is left out.
func fcHref(place placeFilter, measure census.FreeCompanyMeasure) string {
	q := url.Values{}
	if measure == census.FreeCompanyActive {
		q.Set("measure", string(measure))
	}
	if place.Region != "" {
		q.Set("region", place.Region)
	}
	if place.Datacenter != "" {
		q.Set("dc", place.Datacenter)
	}
	if place.World != "" {
		q.Set("world", place.World)
	}
	if len(q) == 0 {
		return fcRankingsPath
	}
	return fcRankingsPath + "?" + q.Encode()
}

func fcMeasureSwitch(place placeFilter, measure census.FreeCompanyMeasure) Switch {
	return Switch{
		Label:  "Rank Free Companies by",
		Target: fcBoardRegion,
		Options: []SwitchOption{
			{Label: "Most members", Href: fcHref(place, census.FreeCompanyMembers), Current: measure == census.FreeCompanyMembers},
			{Label: "Most active members", Href: fcHref(place, census.FreeCompanyActive), Current: measure == census.FreeCompanyActive},
		},
	}
}

// fcFilterBar is the place filter bar; the active board keeps its measure
// through the form and every chip.
func fcFilterBar(place placeFilter, measure census.FreeCompanyMeasure) FilterBar {
	bar := placeFilterBar(fcRankingsPath, "Filter Free Companies", place)
	if measure != census.FreeCompanyActive {
		return bar
	}
	bar.Hidden = append(bar.Hidden, FilterHidden{Name: "measure", Value: string(measure)})
	keep := func(href string) string {
		if strings.Contains(href, "?") {
			return href + "&measure=" + string(measure)
		}
		return href + "?measure=" + string(measure)
	}
	for i := range bar.Chips {
		bar.Chips[i].RemoveHref = keep(bar.Chips[i].RemoveHref)
	}
	bar.ClearHref = keep(bar.ClearHref)
	return bar
}

// fcMembershipLine is the share of a place's characters in any Free Company.
func fcMembershipLine(counts census.LocationCounts, placePhrase string) string {
	if counts.Total == 0 {
		return ""
	}
	return fmt.Sprintf("%s of characters%s belong to a Free Company.", percentOf(float64(counts.InFreeCompany)/float64(counts.Total)), placePhrase)
}

// fcPlacePhrase reads " on Balmung", " in Europe", or "" for the whole game.
func fcPlacePhrase(place placeFilter) string {
	switch {
	case place.World != "":
		return " on " + place.World
	case place.Datacenter != "":
		return " on the " + place.Datacenter + " datacenter"
	case place.Region != "":
		return " in " + regionDisplayName(place.Region)
	}
	return ""
}

func fcScopeLine(place placeFilter) string {
	switch {
	case place.World != "":
		return "On " + place.World
	case place.Datacenter != "":
		return "On the " + place.Datacenter + " datacenter"
	case place.Region != "":
		return "In " + regionDisplayName(place.Region)
	}
	return "Across every world"
}

func fcAnnounceScope(place placeFilter) string {
	switch {
	case place.World != "":
		return place.World
	case place.Datacenter != "":
		return place.Datacenter + " datacenter"
	case place.Region != "":
		return regionDisplayName(place.Region)
	}
	return "every world"
}

func fcPageTitle(place placeFilter) string {
	switch {
	case place.World != "":
		return "Free Companies - " + place.World
	case place.Datacenter != "":
		return "Free Companies - " + place.Datacenter + " DC"
	case place.Region != "":
		return "Free Companies - " + regionDisplayName(place.Region)
	}
	return "Free Company Rankings"
}

// TopFreeCompaniesBlock is a world page's largest Free Companies.
type TopFreeCompaniesBlock struct {
	Ready      bool
	Ladder     Ladder
	Href       string
	Membership string
}

// topFreeCompaniesBlock builds a world page's five largest Free Companies.
func topFreeCompaniesBlock(world string, counts census.LocationCounts, result census.FreeCompanyBoardResult, err error) TopFreeCompaniesBlock {
	block := TopFreeCompaniesBlock{Href: fcHref(placeFilter{World: world}, census.FreeCompanyMembers)}
	block.Membership = fcMembershipLine(counts, " on "+world)
	if err != nil {
		logging.Error("ui.world_free_companies", err.Error())
		return block
	}
	block.Ready = result.Ready
	rows := result.Rows
	if len(rows) > worldTopFreeCompanies {
		rows = rows[:worldTopFreeCompanies]
	}
	block.Ladder = fcLadder(rows, census.FreeCompanyMembers)
	block.Ladder.Caption = "Largest Free Companies, " + world
	block.Ladder.ValueHeading, block.Ladder.DetailHeading = "Members seen", "Active members"
	return block
}
