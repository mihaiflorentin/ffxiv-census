package ui

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// standardHeight is the height of a standard's SVG viewBox; standardWidth its
// width. The fill rises from the swallowtail to share x height.
const (
	standardWidth  = 120
	standardHeight = 320
)

// GrandCompanyView is one company (or "no company") as a page shows it.
type GrandCompanyView struct {
	// Name is the snapshot key; Label is what the page prints ("No Grand
	// Company" for the None group).
	Name, Label, Slug string
	None              bool
	Total, Active     int64
	// Share, ActiveShare and ShareOfActive are formatted percentages; see
	// census.GrandCompanyShare.
	Share, ActiveShare, ShareOfActive string
	// ShareValue and ActiveShareValue are 0-100 for meters and charts.
	ShareValue, ActiveShareValue float64
	// FillY and FillHeight place the standard's fill in its viewBox.
	FillY, FillHeight string
}

// RaceLeanRow is one race's Grand Company split (whole game).
type RaceLeanRow struct {
	Race      string
	Total     int64
	Leader    string
	Companies []GrandCompanyView
}

// GrandCompaniesViewData feeds /ui/grand-companies.
type GrandCompaniesViewData struct {
	// Ready is false while the stored snapshot predates the Grand Company
	// figures (a schema 2 snapshot); the page then shows the next-refresh state.
	Ready    bool
	Filtered bool
	// Scope reads like "across every region" or "on the Chaos datacenter".
	Scope         string
	Total, Active int64
	Companies     []GrandCompanyView
	Leader        GrandCompanyView
	NoCompany     GrandCompanyView
	RaceLean      []RaceLeanRow
	Filters       FilterBar
	// StandardWidth and StandardHeight are the standards' viewBox size.
	StandardWidth, StandardHeight int
}

// GrandCompanyBand is the compact Grand Company split on a world page.
type GrandCompanyBand struct {
	Ready    bool
	Total    int64
	Segments []GrandCompanySegment
	Href     string
}

// GrandCompanySegment is one company's stretch of the band: X and Width are
// percentages of the band's 0-100 viewBox.
type GrandCompanySegment struct {
	GrandCompanyView
	X, Width string
}

// GrandCompanies handles GET /ui/grand-companies.
func (c *UIController) GrandCompanies(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	data := GrandCompaniesViewData{
		Ready:          census.SnapshotHasSection(snapshot, census.SectionGrandCompanies),
		Filtered:       place != (placeFilter{}),
		Scope:          placeScope(place.Region, place.Datacenter, place.World),
		Filters:        placeFilterBar("/ui/grand-companies", "Filter Grand Companies", place),
		StandardWidth:  standardWidth,
		StandardHeight: standardHeight,
	}
	if data.Ready {
		split := census.GrandCompanySplitFor(snapshot, place.scope())
		data.Total, data.Active = split.Total, split.Active
		if split.Total > 0 {
			data.Companies = grandCompanyViews(split)
			data.Leader = newGrandCompanyView(split.Leader())
			data.NoCompany = data.Companies[len(data.Companies)-1]
		}
		for _, lean := range census.GrandCompanyRaceLean(snapshot) {
			data.RaceLean = append(data.RaceLean, RaceLeanRow{
				Race: lean.Race, Total: lean.Total, Leader: lean.Leader().Name, Companies: grandCompanyViews(lean.GrandCompanySplit),
			})
		}
	}

	title := "Grand Companies"
	switch {
	case place.World != "":
		title = fmt.Sprintf("Grand Companies - %s", place.World)
	case place.Datacenter != "":
		title = fmt.Sprintf("Grand Companies - %s DC", place.Datacenter)
	case place.Region != "":
		title = fmt.Sprintf("Grand Companies - %s", regionDisplayName(place.Region))
	}
	c.render(w, "templates/grand_companies.html", statsPageData(title, "grand-companies", "/ui/grand-companies",
		"How Final Fantasy XIV characters split across the Maelstrom, the Order of the Twin Adder and the Immortal Flames: shares, active members and race leanings by region, datacenter and world.",
		state, data))
}

// grandCompanyBand builds a world page's compact Grand Company split.
func grandCompanyBand(split census.GrandCompanySplit, ready bool, world string) GrandCompanyBand {
	band := GrandCompanyBand{Ready: ready, Total: split.Total, Href: "/ui/grand-companies?world=" + url.QueryEscape(world)}
	if !ready || split.Total == 0 {
		return band
	}
	var x float64
	for _, company := range split.Companies {
		band.Segments = append(band.Segments, GrandCompanySegment{
			GrandCompanyView: newGrandCompanyView(company),
			X:                fmt.Sprintf("%.3f", x*100),
			Width:            fmt.Sprintf("%.3f", company.Share*100),
		})
		x += company.Share
	}
	return band
}

func grandCompanyViews(split census.GrandCompanySplit) []GrandCompanyView {
	views := make([]GrandCompanyView, 0, len(split.Companies))
	for _, company := range split.Companies {
		views = append(views, newGrandCompanyView(company))
	}
	return views
}

func newGrandCompanyView(c census.GrandCompanyShare) GrandCompanyView {
	label := c.Name
	if c.Name == census.GrandCompanyNone {
		label = "No Grand Company"
	}
	fill := c.Share * standardHeight
	return GrandCompanyView{
		Name: c.Name, Label: label, Slug: c.Slug, None: c.Name == census.GrandCompanyNone,
		Total: c.Total, Active: c.Active,
		Share:            percentOf(c.Share),
		ActiveShare:      percentOf(c.ActiveShare),
		ShareOfActive:    percentOf(c.ShareOfActive),
		ShareValue:       c.Share * 100,
		ActiveShareValue: c.ActiveShare * 100,
		FillY:            fmt.Sprintf("%.2f", standardHeight-fill),
		FillHeight:       fmt.Sprintf("%.2f", fill),
	}
}

// percentOf formats a 0-1 ratio with one decimal place, like formatPercent.
func percentOf(ratio float64) string {
	return fmt.Sprintf("%.1f%%", ratio*100)
}
