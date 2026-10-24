package ui

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// armouryRegion is the id of the region the "Any level / At the level cap"
// switch swaps.
const armouryRegion = "armoury"

// armouryLevels is how many fill steps a tile has above empty.
const armouryLevels = 5

// JobTileView is one job tile of the Armoury.
type JobTileView struct {
	Abbreviation, Name string
	// Count and Share are the selected measure's figures; Share is of the
	// characters the figures cover.
	Count      int64
	Share      string
	ShareValue float64
	// Level (0-5) is the tile's fill step: the count relative to the most
	// common job of the measure. Gauge is the share as a 0-100 width.
	Level int
	Gauge string
	// Lead marks the most common job of the measure.
	Lead bool
	// Levelled and AtCap feed the numbers table.
	Levelled, AtCap           int64
	LevelledShare, AtCapShare string
	LevelledValue, AtCapValue float64
}

// ArmouryRoleView is one role group of the Armoury.
type ArmouryRoleView struct {
	Code, Name string
	// Mark is the role colour of the heading rule: tank, healer, dps, or ""
	// for the crafting and gathering disciplines.
	Mark string
	Jobs []JobTileView
}

// RoleSegment is one role's stretch of a balance bar: X and Width are
// percentages of the bar's 0-100 viewBox.
type RoleSegment struct {
	Mark, Label string
	Count       int64
	Share       string
	X, Width    string
}

// RoleBalanceView is a party balance at the level cap (a datacenter or the
// selection).
type RoleBalanceView struct {
	Name     string
	Region   string
	Current  bool
	Total    int64
	Segments []RoleSegment
	// PerTank reads like "1 tank to 0.9 healers and 3.4 DPS".
	PerTank string
}

// JobsViewData feeds /ui/jobs.
type JobsViewData struct {
	// Ready is false while the stored snapshot has no job section.
	Ready    bool
	Filtered bool
	Scope    string
	// Measure is "levelled" (shown as "any level") or "at-cap";
	// MeasurePhrase reads "at any level" or "at the level cap".
	Measure       string
	MeasurePhrase string
	AtCap         bool
	BuiltAt       time.Time
	// Characters and Covered describe the figures' coverage; Partial is true
	// while some characters are not covered yet.
	Characters, Covered int64
	Partial             bool
	Combat, Crafting    []ArmouryRoleView
	Lead                JobTileView
	Balance             RoleBalanceView
	DatacenterBalances  []RoleBalanceView
	BalanceArea         string
	Switch              Switch
	Filters             FilterBar
}

// TopJobsBlock is the world page's most common jobs at any level.
type TopJobsBlock struct {
	Ready bool
	Jobs  []JobTileView
	Href  string
}

// Jobs handles GET /ui/jobs.
func (c *UIController) Jobs(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	place := parsePlaceFilter(r)
	measure := census.MeasureLevelled
	if r.URL.Query().Get("measure") == string(census.MeasureAtCap) {
		measure = census.MeasureAtCap
	}
	data := JobsViewData{
		Ready:         census.SnapshotHasSection(snapshot, census.SectionJobs),
		Filtered:      place != (placeFilter{}),
		Scope:         placeScope(place.Region, place.Datacenter, place.World),
		Measure:       string(measure),
		MeasurePhrase: "at any level",
		AtCap:         measure == census.MeasureAtCap,
		Switch:        jobsMeasureSwitch(place, measure),
		Filters:       placeFilterBar("/ui/jobs", "Filter jobs", place),
	}
	if data.AtCap {
		data.MeasurePhrase = "at the level cap"
	}
	if data.Ready {
		armoury := c.memo.Armoury(snapshot, place.scope())
		data.BuiltAt = armoury.BuiltAt
		data.Characters, data.Covered = armoury.Characters, armoury.Covered
		data.Partial = !armoury.FullyCovered()
		if armoury.HasData() {
			for _, role := range armoury.Roles {
				view := newArmouryRoleView(role, measure)
				if role.Code == census.RoleDisciplesOfTheHand || role.Code == census.RoleDisciplesOfTheLand {
					data.Crafting = append(data.Crafting, view)
				} else {
					data.Combat = append(data.Combat, view)
				}
				for _, tile := range view.Jobs {
					if tile.Lead && data.Lead.Abbreviation == "" {
						data.Lead = tile
					}
				}
			}
			data.Balance = newRoleBalanceView(placeName(place), "", armoury.Balance, false)
		}
		balanceRegion := place.Region
		if balanceRegion == "" && place.Datacenter != "" {
			balanceRegion = census.RegionForDatacenter(place.Datacenter)
		}
		if balanceRegion == "" && place.World != "" {
			balanceRegion = census.RegionForDatacenter(census.WorldDatacenter(place.World))
		}
		data.BalanceArea = "every datacenter"
		if balanceRegion != "" {
			data.BalanceArea = "datacenters in " + regionDisplayName(balanceRegion)
		}
		currentDC := place.Datacenter
		if currentDC == "" && place.World != "" {
			currentDC = census.WorldDatacenter(place.World)
		}
		for _, dc := range census.RoleBalanceByDatacenter(snapshot, balanceRegion) {
			data.DatacenterBalances = append(data.DatacenterBalances,
				newRoleBalanceView(dc.Datacenter, dc.Region, dc.RoleBalance, dc.Datacenter == currentDC))
		}
	}

	if wantsSwitchRegion(r, armouryRegion) {
		c.renderRegion(w, "templates/jobs.html", "armoury", PageData{Data: data})
		return
	}
	title := "Jobs"
	switch {
	case place.World != "":
		title = fmt.Sprintf("Jobs - %s", place.World)
	case place.Datacenter != "":
		title = fmt.Sprintf("Jobs - %s DC", place.Datacenter)
	case place.Region != "":
		title = fmt.Sprintf("Jobs - %s", regionDisplayName(place.Region))
	}
	c.render(w, "templates/jobs.html", statsPageData(title, "jobs", "/ui/jobs",
		"Which Final Fantasy XIV jobs characters have at any level and which they have at the level cap, by role, with the tank, healer and DPS balance of every datacenter.",
		state, data))
}

// jobsMeasureSwitch builds the "Any level / At the level cap" switch; both
// links keep the location filter.
func jobsMeasureSwitch(place placeFilter, measure census.ArmouryMeasure) Switch {
	href := func(m census.ArmouryMeasure) string {
		q := url.Values{}
		if place.Region != "" {
			q.Set("region", place.Region)
		}
		if place.Datacenter != "" {
			q.Set("dc", place.Datacenter)
		}
		if place.World != "" {
			q.Set("world", place.World)
		}
		if m != census.MeasureLevelled {
			q.Set("measure", string(m))
		}
		if len(q) == 0 {
			return "/ui/jobs"
		}
		return "/ui/jobs?" + q.Encode()
	}
	return Switch{
		Label:  "Count jobs",
		Target: armouryRegion,
		Options: []SwitchOption{
			{Label: "Any level", Href: href(census.MeasureLevelled), Current: measure == census.MeasureLevelled},
			{Label: "At the level cap", Href: href(census.MeasureAtCap), Current: measure == census.MeasureAtCap},
		},
	}
}

// placeName names a selection for the balance summary.
func placeName(place placeFilter) string {
	switch {
	case place.World != "":
		return place.World
	case place.Datacenter != "":
		return place.Datacenter
	case place.Region != "":
		return regionDisplayName(place.Region)
	}
	return "The whole game"
}

func roleMark(code string) string {
	switch code {
	case census.RoleTank:
		return "tank"
	case census.RoleHealer:
		return "healer"
	case census.RoleMeleeDPS, census.RolePhysicalRangedDPS, census.RoleMagicalRangedDPS:
		return "dps"
	}
	return ""
}

func newArmouryRoleView(role census.ArmouryRole, measure census.ArmouryMeasure) ArmouryRoleView {
	view := ArmouryRoleView{Code: role.Code, Name: role.Name, Mark: roleMark(role.Code)}
	for _, j := range role.Jobs {
		view.Jobs = append(view.Jobs, newJobTileView(j, measure))
	}
	return view
}

func newJobTileView(j census.ArmouryJob, measure census.ArmouryMeasure) JobTileView {
	intensity := j.Intensity(measure)
	level := 0
	if j.Count(measure) > 0 {
		level = int(math.Ceil(intensity * armouryLevels))
		level = max(1, min(armouryLevels, level))
	}
	return JobTileView{
		Abbreviation: j.Abbreviation, Name: j.Name,
		Count: j.Count(measure), Share: percentOf(j.Share(measure)), ShareValue: j.Share(measure) * 100,
		Level: level, Gauge: fmt.Sprintf("%.2f", j.Share(measure)*100),
		Lead:     j.Count(measure) > 0 && intensity >= 1,
		Levelled: j.Levelled, AtCap: j.AtCap,
		LevelledShare: percentOf(j.Share(census.MeasureLevelled)), AtCapShare: percentOf(j.Share(census.MeasureAtCap)),
		LevelledValue: j.Share(census.MeasureLevelled) * 100, AtCapValue: j.Share(census.MeasureAtCap) * 100,
	}
}

func newRoleBalanceView(name, region string, b census.RoleBalance, current bool) RoleBalanceView {
	view := RoleBalanceView{Name: name, Region: region, Current: current, Total: b.Total()}
	if b.Total() == 0 {
		return view
	}
	var x float64
	for _, seg := range []struct {
		mark, label string
		count       int64
		share       float64
	}{
		{"tank", "Tanks", b.Tank, b.TankShare()},
		{"healer", "Healers", b.Healer, b.HealerShare()},
		{"dps", "DPS", b.DPS, b.DPSShare()},
	} {
		view.Segments = append(view.Segments, RoleSegment{
			Mark: seg.mark, Label: seg.label, Count: seg.count, Share: percentOf(seg.share),
			X: fmt.Sprintf("%.3f", x*100), Width: fmt.Sprintf("%.3f", seg.share*100),
		})
		x += seg.share
	}
	if b.Tank > 0 {
		view.PerTank = fmt.Sprintf("1 tank to %.1f healers and %.1f DPS", float64(b.Healer)/float64(b.Tank), float64(b.DPS)/float64(b.Tank))
	}
	return view
}

// topJobsBlock builds a world page's five most-levelled jobs.
func topJobsBlock(armoury census.Armoury, ready bool, world string) TopJobsBlock {
	block := TopJobsBlock{Ready: ready, Href: "/ui/jobs?world=" + url.QueryEscape(world)}
	for _, j := range armoury.MostLevelled(5) {
		block.Jobs = append(block.Jobs, newJobTileView(j, census.MeasureLevelled))
	}
	return block
}
