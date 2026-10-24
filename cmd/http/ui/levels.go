package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// levelSteps is how many steps the level band ramp has (--color-level-1 to
// --color-level-7 in styles.css). The level cap band always takes the
// brightest step; with more bands than steps the oldest bands share the
// first.
const levelSteps = 7

// levelStep is the ramp step (1-levelSteps) of band i of n.
func levelStep(i, n int) int {
	return max(1, levelSteps-(n-1-i))
}

// levelRange prints a band's levels: "1–49", or "100" for the open cap band.
func levelRange(b contract.StatsLevelBand) string {
	if b.Max == 0 {
		return fmt.Sprintf("%d", b.Min)
	}
	return fmt.Sprintf("%d–%d", b.Min, b.Max)
}

// LevelBandKey is one entry of a level chart's key.
type LevelBandKey struct {
	Step        int
	Name, Range string
	// Top is the band's highest level ("49"; the cap band's own level).
	Top string
}

// LevelBandView is one band of a location: a row of the band chart, the
// key with shares, and the numbers table.
type LevelBandView struct {
	LevelBandKey
	Count int64
	Share string
	// Bar is the band's count against the largest band's, as a 0-100 width.
	Bar string
}

// LevelSegment is one band's stretch of a stacked row: X and Width are
// percentages of the row's 0-100 viewBox. Empty bands have no segment.
type LevelSegment struct {
	LevelBandKey
	Count    int64
	Share    string
	X, Width string
}

// LevelRowView is one datacenter or world in the stacked rows, or a world
// page's strip.
type LevelRowView struct {
	Name, Region, Href string
	Current            bool
	Total, Levelled    int64
	NoLevels           int64
	Segments           []LevelSegment
	Bands              []LevelBandView
	// First and Cap are the lowest and the level cap band, for the summary
	// sentences and the row's cap share.
	First, Cap LevelBandView
	// Lede is the summary sentence about the largest band.
	Lede LevelLede
}

// LevelLede reads "<Share> of characters <Text>. <Aside>": Share is the
// largest band's, Text what that band means ("never took a job past level
// 49", "have a job at the level cap", "peaked in Endwalker, levels 80–89"),
// and Aside the other end of the range.
type LevelLede struct {
	Share, Text, Aside string
}

func levelLede(row LevelRowView) LevelLede {
	lead := 0
	for i, b := range row.Bands {
		if b.Count > row.Bands[lead].Count {
			lead = i
		}
	}
	never := "never took a job past level " + row.First.Top
	atCap := "have a job at the level cap"
	b := row.Bands[lead]
	switch lead {
	case 0:
		return LevelLede{Share: b.Share, Text: never, Aside: row.Cap.Share + " " + atCap + "."}
	case len(row.Bands) - 1:
		return LevelLede{Share: b.Share, Text: atCap, Aside: row.First.Share + " " + never + "."}
	}
	return LevelLede{Share: b.Share, Text: "peaked in " + b.Name + ", levels " + b.Range, Aside: row.Cap.Share + " " + atCap + "."}
}

// RegionLink is a link that narrows a page to one region.
type RegionLink struct {
	Name, Href string
}

// LevelsViewData feeds /ui/levels.
type LevelsViewData struct {
	// Ready is false while the stored snapshot has no level figures.
	Ready    bool
	Filtered bool
	Scope    string
	BuiltAt  time.Time
	Overall  LevelRowView
	Key      []LevelBandKey
	// DatacenterArea and WorldArea describe the rows ("every datacenter",
	// "worlds on the Crystal datacenter"); WorldPrompt offers each region
	// while no place is selected.
	DatacenterArea string
	Datacenters    []LevelRowView
	WorldArea      string
	Worlds         []LevelRowView
	WorldPrompt    []RegionLink
	Filters        FilterBar
}

// LevelStripBlock is the world page's compact level band.
type LevelStripBlock struct {
	Ready bool
	Row   LevelRowView
	Key   []LevelBandKey
	Href  string
}

// Levels handles GET /ui/levels.
func (c *UIController) Levels(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	data := LevelsViewData{
		Ready:    census.LevelsReady(snapshot),
		Filtered: place != (placeFilter{}),
		Scope:    placeScope(place.Region, place.Datacenter, place.World),
		Filters:  placeFilterBar("/ui/levels", "Filter levels", place),
	}
	if data.Ready {
		data.BuiltAt = snapshot.LevelsBuiltAt
		data.Key = levelKey(snapshot.LevelBands)
		data.Overall = newLevelRow(c.memo.Levels(snapshot, place.scope()), placeName(place), "", false)

		region := place.Region
		dc := place.Datacenter
		if place.World != "" {
			dc = census.WorldDatacenter(place.World)
		}
		if region == "" && dc != "" {
			region = census.RegionForDatacenter(dc)
		}
		data.DatacenterArea = "every datacenter"
		if region != "" {
			data.DatacenterArea = "datacenters in " + regionDisplayName(region)
		}
		for _, d := range c.memo.LevelsByDatacenter(snapshot, region) {
			row := newLevelRow(d, d.Key, d.Region, d.Key == dc)
			row.Href = "/ui/levels?dc=" + url.QueryEscape(d.Key) + "#level-worlds"
			data.Datacenters = append(data.Datacenters, row)
		}

		var worldScope contract.StatsScope
		switch {
		case dc != "":
			worldScope = contract.StatsScope{Datacenter: dc}
			data.WorldArea = "worlds on the " + dc + " datacenter"
		case region != "":
			worldScope = contract.StatsScope{Region: region}
			data.WorldArea = "worlds in " + regionDisplayName(region)
		}
		if worldScope == (contract.StatsScope{}) {
			for _, r := range census.Regions() {
				data.WorldPrompt = append(data.WorldPrompt, RegionLink{Name: r.Name, Href: "/ui/levels?region=" + r.Code + "#level-worlds"})
			}
		} else {
			for _, d := range c.memo.LevelsByWorld(snapshot, worldScope) {
				row := newLevelRow(d, d.Key, "", d.Key == place.World)
				row.Href = "/ui/worlds/" + url.PathEscape(d.Key)
				data.Worlds = append(data.Worlds, row)
			}
		}
	}

	title := "Levels"
	switch {
	case place.World != "":
		title = fmt.Sprintf("Levels - %s", place.World)
	case place.Datacenter != "":
		title = fmt.Sprintf("Levels - %s DC", place.Datacenter)
	case place.Region != "":
		title = fmt.Sprintf("Levels - %s", regionDisplayName(place.Region))
	}
	c.render(w, "templates/levels.html", statsPageData(title, "levels", "/ui/levels",
		"How far Final Fantasy XIV characters have levelled: every character placed in the expansion its highest job reached, for the whole game, every datacenter and every world.",
		state, data))
}

func newLevelBandKey(i, n int, b contract.StatsLevelBand) LevelBandKey {
	top := b.Max
	if top == 0 {
		top = b.Min
	}
	return LevelBandKey{Step: levelStep(i, n), Name: b.Name, Range: levelRange(b), Top: fmt.Sprintf("%d", top)}
}

// levelKey is the key of the level charts, in band order.
func levelKey(bands []contract.StatsLevelBand) []LevelBandKey {
	key := make([]LevelBandKey, len(bands))
	for i, b := range bands {
		key[i] = newLevelBandKey(i, len(bands), b)
	}
	return key
}

func newLevelRow(d census.LevelDistribution, name, region string, current bool) LevelRowView {
	row := LevelRowView{Name: name, Region: region, Current: current, Total: d.Total, Levelled: d.Levelled(), NoLevels: d.NoLevels}
	var largest int64
	for _, b := range d.Bands {
		largest = max(largest, b.Count)
	}
	var x float64
	for i, b := range d.Bands {
		key := newLevelBandKey(i, len(d.Bands), b.StatsLevelBand)
		view := LevelBandView{LevelBandKey: key, Count: b.Count, Share: percentOf(b.Share), Bar: "0"}
		if largest > 0 {
			view.Bar = fmt.Sprintf("%.3f", float64(b.Count)/float64(largest)*100)
		}
		row.Bands = append(row.Bands, view)
		if b.Count > 0 {
			row.Segments = append(row.Segments, LevelSegment{
				LevelBandKey: key, Count: b.Count, Share: view.Share,
				X: fmt.Sprintf("%.3f", x*100), Width: fmt.Sprintf("%.3f", b.Share*100),
			})
		}
		x += b.Share
	}
	if n := len(row.Bands); n > 0 {
		row.First, row.Cap = row.Bands[0], row.Bands[n-1]
		row.Lede = levelLede(row)
	}
	return row
}

// levelStripBlock builds a world page's compact level band.
func levelStripBlock(snapshot *contract.UIStatsSnapshot, d census.LevelDistribution, world string) LevelStripBlock {
	block := LevelStripBlock{Ready: census.LevelsReady(snapshot), Href: "/ui/levels?world=" + url.QueryEscape(world)}
	if block.Ready {
		block.Key = levelKey(snapshot.LevelBands)
		block.Row = newLevelRow(d, world, "", false)
	}
	return block
}
