package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// rankingsBoardRegion is the id of the board the Rankings switches swap.
const rankingsBoardRegion = "rankings-board"

// rankingsDefault is the board /ui/rankings opens on, and what unknown
// parameter values fall back to.
var rankingsDefault = census.RankingQuery{Metric: census.MetricGrowth, By: census.LocationWorld}

// rankingMetricView is how the Rankings page presents one metric.
type rankingMetricView struct {
	metric census.RankingMetric
	// group is the metric's option on the "Rank by" switch; a group with
	// several metrics adds a second switch of variants.
	group string
	// variant labels the metric on its group's variant switch.
	variant string
	// title is the board heading for a noun ("worlds" or "datacenters").
	title func(noun string) string
	// explain is the one-line explanation for a place ("world").
	explain func(place string, facts rankingFacts) string
	// rung formats a row's value, unit and detail line.
	rung func(row census.RankingRow, maxLevel int) (value, unit, detail string)
	// pending names the figures a snapshot without them is waiting for.
	pending string
	// nightly metrics show when their figures were rebuilt.
	nightly bool
	// tone colours the ladder (see Ladder.Tone).
	tone string
}

// rankingGroup is one option of the "Rank by" switch.
type rankingGroup struct {
	key, label, variantLabel string
}

var rankingGroups = []rankingGroup{
	{"growth", "Growth", "Count growth as"},
	{"activity", "Activity", ""},
	{"veterans", "Veterans", ""},
	{"population", "Population", ""},
	{"privacy", "Privacy", ""},
	{"grand-companies", "Grand Companies", "Grand Company"},
}

func rateRung(what string) func(census.RankingRow, int) (string, string, string) {
	return func(row census.RankingRow, _ int) (string, string, string) {
		return percentOf(row.Value), "", fmt.Sprintf("%s of %s %s", formatNumber(row.Part), formatNumber(row.Total), what)
	}
}

// companyLean is the view of one Grand Company's lean metric, named from
// the domain's Grand Company table.
func companyLean(metric census.RankingMetric) rankingMetricView {
	gc, _ := census.LeanCompany(metric)
	return rankingMetricView{
		tone:   gc.Slug,
		metric: metric, group: "grand-companies", variant: gc.Short,
		title: func(string) string { return "Strongest " + gc.Short + " lean" },
		explain: func(place string, _ rankingFacts) string {
			return fmt.Sprintf("Share of the %s’s characters enlisted in the %s, out of everyone including those in no Grand Company.", place, gc.Name)
		},
		rung:    rateRung("in the " + gc.Name),
		pending: "Grand Company figures",
	}
}

// rankingFacts are the snapshot facts a metric's explanation may quote.
type rankingFacts struct {
	MaxLevel            int
	ActivityWindow      Window
	NewCharactersWindow Window
}

var rankingMetricViews = []rankingMetricView{
	{
		metric: census.MetricGrowth, group: "growth", variant: "Per 1,000 residents",
		title: func(noun string) string { return "Fastest-growing " + noun },
		explain: func(place string, facts rankingFacts) string {
			return fmt.Sprintf("New characters in the last %s for every 1,000 characters on the %s.", facts.NewCharactersWindow.Long(), place)
		},
		rung: func(row census.RankingRow, _ int) (string, string, string) {
			return fmt.Sprintf("%.1f", row.Value), "per 1,000", fmt.Sprintf("%s new of %s characters", formatNumber(row.Part), formatNumber(row.Total))
		},
	},
	{
		metric: census.MetricGrowthCount, group: "growth", variant: "New characters",
		title: func(string) string { return "Most new characters" },
		explain: func(_ string, facts rankingFacts) string {
			return fmt.Sprintf("Characters who earned their first company chocobo in the last %s, the census’s count of new characters.", facts.NewCharactersWindow.Long())
		},
		rung: func(row census.RankingRow, _ int) (string, string, string) {
			return formatNumber(row.Part), "new", fmt.Sprintf("Of %s characters", formatNumber(row.Total))
		},
	},
	{
		metric: census.MetricActivity, group: "activity",
		title: func(noun string) string { return "Most active " + noun },
		explain: func(_ string, facts rankingFacts) string {
			return fmt.Sprintf("Share of characters who earned a public achievement in the last %s.", facts.ActivityWindow.Long())
		},
		rung: rateRung("active"),
	},
	{
		metric: census.MetricVeterans, group: "veterans",
		title: func(string) string { return "Most veterans" },
		explain: func(_ string, facts rankingFacts) string {
			return fmt.Sprintf("Share of characters with a job at the level cap, level %d.", facts.MaxLevel)
		},
		rung: func(row census.RankingRow, maxLevel int) (string, string, string) {
			return rateRung(fmt.Sprintf("at level %d", maxLevel))(row, maxLevel)
		},
		pending: "Level cap figures",
	},
	{
		metric: census.MetricPopulation, group: "population",
		title: func(noun string) string { return "Largest " + noun },
		explain: func(place string, _ rankingFacts) string {
			return fmt.Sprintf("Characters the census has found on the %s.", place)
		},
		rung: func(row census.RankingRow, _ int) (string, string, string) {
			return formatNumber(row.Part), "characters", ""
		},
	},
	{
		metric: census.MetricPrivacy, group: "privacy",
		title: func(noun string) string { return "Most private " + noun },
		explain: func(string, rankingFacts) string {
			return "Share of characters who hide their achievements from the Lodestone."
		},
		rung:    rateRung("private"),
		pending: "Privacy figures",
		nightly: true,
	},
	companyLean(census.MetricLeanMaelstrom),
	companyLean(census.MetricLeanTwinAdder),
	companyLean(census.MetricLeanImmortalFlames),
}

func rankingMetricViewFor(metric census.RankingMetric) rankingMetricView {
	for _, v := range rankingMetricViews {
		if v.metric == metric {
			return v
		}
	}
	return rankingMetricViews[0]
}

// RankingsTab is one of the "Worlds and datacenters | Free Companies" links
// at the top of the Rankings pages. A Hidden tab is not rendered yet.
type RankingsTab struct {
	Label, Href string
	Current     bool
	Hidden      bool
}

// rankingsTabs lists the Rankings boards.
func rankingsTabs(current string) []RankingsTab {
	return []RankingsTab{
		{Label: "Worlds and datacenters", Href: "/ui/rankings", Current: current == "worlds"},
		{Label: "Free Companies", Href: "/ui/rankings/free-companies", Current: current == "free-companies"},
	}
}

// RankingsViewData feeds /ui/rankings.
type RankingsViewData struct {
	Tabs []RankingsTab
	// Title heads the board ("Fastest-growing worlds"); Scope reads
	// "Across every region" or "In Europe"; Explanation is the metric's one
	// line; Announce is what a screen reader hears after a switch.
	Title, Scope, Explanation, Announce string
	MetricSwitch                        Switch
	// VariantSwitch is set for a metric with variants (growth, Grand Company).
	VariantSwitch *Switch
	BySwitch      Switch
	RegionSwitch  Switch
	// Ready is false while the snapshot predates the metric's figures;
	// Pending then names them for the next-refresh state.
	Ready   bool
	Pending string
	Ladder  Ladder
	// Excluded entries had fewer than MinimumSize characters; Noun and
	// NounOne name them ("worlds", "world").
	Excluded       int
	MinimumSize    int64
	Noun, NounOne  string
	BuiltAt        time.Time
	AllRegionsHref string
}

// Rankings handles GET /ui/rankings: one board of worlds or datacenters.
func (c *UIController) Rankings(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	query := parseRankingQuery(r)
	view := rankingMetricViewFor(query.Metric)
	place, noun := "world", "worlds"
	if query.By == census.LocationDatacenter {
		place, noun = "datacenter", "datacenters"
	}
	scope := "Across every region"
	announceScope := "every region"
	if query.Region != "" {
		scope = "In " + regionDisplayName(query.Region)
		announceScope = regionDisplayName(query.Region)
	}
	title := view.title(noun)
	data := RankingsViewData{
		Tabs:           rankingsTabs("worlds"),
		Title:          title,
		Scope:          scope,
		Explanation:    view.explain(place, rankingFacts{MaxLevel: int(snapshot.MaxLevel), ActivityWindow: Window{Days: state.ActivityWindowDays}, NewCharactersWindow: Window{Days: state.NewCharactersWindowDays}}),
		Announce:       title + ", " + announceScope,
		MetricSwitch:   rankingsMetricSwitch(query),
		VariantSwitch:  rankingsVariantSwitch(query, view.group),
		BySwitch:       rankingsBySwitch(query),
		RegionSwitch:   rankingsRegionSwitch(query),
		Pending:        view.pending,
		Noun:           noun,
		NounOne:        place,
		AllRegionsHref: rankingsHref(census.RankingQuery{Metric: query.Metric, By: query.By}),
	}
	board := c.memo.Rankings(snapshot, query)
	data.Ready = board.Ready
	if board.Ready {
		data.Excluded, data.MinimumSize = board.Excluded, board.MinimumSize
		if view.nightly {
			data.BuiltAt = snapshot.PrivacyBuiltAt
		}
		rungs := make([]LadderRung, 0, len(board.Rows))
		for _, row := range board.Rows {
			value, unit, detail := view.rung(row, int(snapshot.MaxLevel))
			rung := LadderRung{
				Rank: row.Rank, Name: row.Key, Note: row.Parent,
				Value: value, Unit: unit, Detail: detail,
				Bar: barWidth(row.ShareOfLeader), Leader: row.Rank == 1,
			}
			if query.By == census.LocationWorld {
				rung.Href = "/ui/worlds/" + url.PathEscape(row.Key)
			} else {
				rung.Note = regionDisplayName(row.Parent)
			}
			rungs = append(rungs, rung)
		}
		data.Ladder = newLadder(rungs, noun)
		data.Ladder.Caption = title + ", " + announceScope
		data.Ladder.NameHeading = strings.ToUpper(place[:1]) + place[1:]
		data.Ladder.NoteHeading = "Datacenter"
		if query.By == census.LocationDatacenter {
			data.Ladder.NoteHeading = "Region"
		}
		data.Ladder.ValueHeading = title
		data.Ladder.DetailHeading = "Detail"
		data.Ladder.Entrance = true
		data.Ladder.Tone = view.tone
	}
	page := statsPageData("Rankings", "rankings", "/ui/rankings",
		"Final Fantasy XIV worlds and datacenters ranked by growth, activity, veterans at the level cap, population, hidden achievements and Grand Company lean, for every region.",
		state, data)
	if wantsSwitchRegion(r, rankingsBoardRegion) {
		c.renderRegion(w, "templates/rankings.html", rankingsBoardRegion, page)
		return
	}
	c.render(w, "templates/rankings.html", page)
}

// parseRankingQuery reads metric, by and region; unknown values fall back
// to the default board (growth, worlds, every region).
func parseRankingQuery(r *http.Request) census.RankingQuery {
	q := r.URL.Query()
	query := rankingsDefault
	metric := census.RankingMetric(strings.ToLower(strings.TrimSpace(q.Get("metric"))))
	for _, v := range rankingMetricViews {
		if v.metric == metric {
			query.Metric = metric
		}
	}
	if strings.EqualFold(strings.TrimSpace(q.Get("by")), string(census.LocationDatacenter)) {
		query.By = census.LocationDatacenter
	}
	if region := census.CanonicalRegion(strings.TrimSpace(q.Get("region"))); census.RegionRank(region) > 0 {
		query.Region = region
	}
	return query
}

// rankingsHref is the canonical link to a board: default values are left out.
func rankingsHref(query census.RankingQuery) string {
	q := url.Values{}
	if query.Metric != rankingsDefault.Metric {
		q.Set("metric", string(query.Metric))
	}
	if query.By != rankingsDefault.By {
		q.Set("by", string(query.By))
	}
	if query.Region != "" {
		q.Set("region", query.Region)
	}
	if len(q) == 0 {
		return "/ui/rankings"
	}
	return "/ui/rankings?" + q.Encode()
}

func rankingsSwitch(label string, options []SwitchOption) Switch {
	return Switch{Label: label, Target: rankingsBoardRegion, Options: options}
}

func rankingsMetricSwitch(query census.RankingQuery) Switch {
	current := rankingMetricViewFor(query.Metric).group
	var options []SwitchOption
	for _, g := range rankingGroups {
		// A group opens on its first metric.
		var first census.RankingMetric
		for _, v := range rankingMetricViews {
			if v.group == g.key {
				first = v.metric
				break
			}
		}
		next := query
		next.Metric = first
		options = append(options, SwitchOption{Label: g.label, Href: rankingsHref(next), Current: g.key == current})
	}
	return rankingsSwitch("Rank by", options)
}

func rankingsVariantSwitch(query census.RankingQuery, group string) *Switch {
	var label string
	for _, g := range rankingGroups {
		if g.key == group {
			label = g.variantLabel
		}
	}
	if label == "" {
		return nil
	}
	var options []SwitchOption
	for _, v := range rankingMetricViews {
		if v.group != group {
			continue
		}
		next := query
		next.Metric = v.metric
		options = append(options, SwitchOption{Label: v.variant, Href: rankingsHref(next), Current: v.metric == query.Metric})
	}
	s := rankingsSwitch(label, options)
	return &s
}

func rankingsBySwitch(query census.RankingQuery) Switch {
	worlds, dcs := query, query
	worlds.By, dcs.By = census.LocationWorld, census.LocationDatacenter
	return rankingsSwitch("Rank", []SwitchOption{
		{Label: "Worlds", Href: rankingsHref(worlds), Current: query.By == census.LocationWorld},
		{Label: "Datacenters", Href: rankingsHref(dcs), Current: query.By == census.LocationDatacenter},
	})
}

func rankingsRegionSwitch(query census.RankingQuery) Switch {
	all := query
	all.Region = ""
	options := []SwitchOption{{Label: "All regions", Href: rankingsHref(all), Current: query.Region == ""}}
	for _, region := range census.Regions() {
		next := query
		next.Region = region.Code
		options = append(options, SwitchOption{Label: region.Name, Href: rankingsHref(next), Current: query.Region == region.Code})
	}
	return rankingsSwitch("Region", options)
}
