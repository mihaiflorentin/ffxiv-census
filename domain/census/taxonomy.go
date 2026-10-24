package census

import "strings"

// The census taxonomy: the fixed order of the playable races, the clans
// (Lodestone "tribes") of each race, and the regions with their display
// names and order. Every page and chart reads these from here so a race or
// region sorts (and is coloured) the same way everywhere.

// raceOrder is the playable races in the game's own order.
var raceOrder = []string{"Hyur", "Elezen", "Lalafell", "Miqo'te", "Roegadyn", "Au Ra", "Hrothgar", "Viera"}

// RaceOrder returns the playable races in the game's order.
func RaceOrder() []string {
	return append([]string(nil), raceOrder...)
}

// RaceRank returns a race's 1-based position in RaceOrder, or 0 for a race
// outside it.
func RaceRank(race string) int {
	for i, r := range raceOrder {
		if r == race {
			return i + 1
		}
	}
	return 0
}

// clanRace maps each clan to its race. Lodestone spells some clans in the
// plural, so both spellings are listed.
var clanRace = map[string]string{
	"Midlander": "Hyur", "Highlander": "Hyur",
	"Wildwood": "Elezen", "Duskwight": "Elezen",
	"Plainsfolk": "Lalafell", "Dunesfolk": "Lalafell",
	"Seeker of the Sun": "Miqo'te", "Seekers of the Sun": "Miqo'te",
	"Keeper of the Moon": "Miqo'te", "Keepers of the Moon": "Miqo'te",
	"Sea Wolf": "Roegadyn", "Sea Wolves": "Roegadyn", "Hellsguard": "Roegadyn",
	"Raen": "Au Ra", "Xaela": "Au Ra",
	"Helions": "Hrothgar", "The Lost": "Hrothgar",
	"Rava": "Viera", "Veena": "Viera",
}

// ClanRace returns the race a clan belongs to, or "" for an unknown clan.
func ClanRace(clan string) string {
	return clanRace[clan]
}

// Region is one game region: its snapshot code and display name.
type Region struct {
	Code string
	Name string
}

// regions lists the regions in display order.
var regions = []Region{
	{Code: "NA", Name: "North America"},
	{Code: "EU", Name: "Europe"},
	{Code: "JP", Name: "Japan"},
	{Code: "OCE", Name: "Oceania"},
}

// Regions returns every region in display order.
func Regions() []Region {
	return append([]Region(nil), regions...)
}

// RegionName returns a region code's display name; an unknown code is its
// own name.
func RegionName(code string) string {
	for _, r := range regions {
		if r.Code == code {
			return r.Name
		}
	}
	return code
}

// RegionRank returns a region code's 1-based display position, or 0 for an
// unknown code.
func RegionRank(code string) int {
	for i, r := range regions {
		if r.Code == code {
			return i + 1
		}
	}
	return 0
}

// CanonicalRegion resolves a region code case-insensitively ("na" -> "NA");
// unknown input passes through unchanged.
func CanonicalRegion(region string) string {
	for _, r := range regions {
		if strings.EqualFold(r.Code, region) {
			return r.Code
		}
	}
	return region
}

// CanonicalDatacenter resolves a datacenter name case-insensitively
// ("chaos" -> "Chaos"); unknown input passes through unchanged.
func CanonicalDatacenter(dc string) string {
	if _, ok := datacenterRegion[dc]; ok {
		return dc
	}
	for known := range datacenterRegion {
		if strings.EqualFold(known, dc) {
			return known
		}
	}
	return dc
}
