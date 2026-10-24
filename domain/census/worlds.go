package census

import (
	"sort"
	"strings"
)

// worldDatacenters maps standard FFXIV worlds to their logical datacenters.
// It completes the world hierarchy: worlds roll up to datacenters (this map)
// and datacenters roll up to regions (see datacenterRegion).
var worldDatacenters = map[string]string{
	// NA - Aether
	"Adamantoise":  "Aether",
	"Cactuar":      "Aether",
	"Faerie":       "Aether",
	"Gilgamesh":    "Aether",
	"Jenova":       "Aether",
	"Midgardsormr": "Aether",
	"Sargatanas":   "Aether",
	"Siren":        "Aether",

	// NA - Primal
	"Behemoth":  "Primal",
	"Excalibur": "Primal",
	"Exodus":    "Primal",
	"Famfrit":   "Primal",
	"Hyperion":  "Primal",
	"Lamia":     "Primal",
	"Leviathan": "Primal",
	"Ultros":    "Primal",

	// NA - Crystal
	"Balmung":   "Crystal",
	"Brynhildr": "Crystal",
	"Coeurl":    "Crystal",
	"Diabolos":  "Crystal",
	"Goblin":    "Crystal",
	"Malboro":   "Crystal",
	"Mateus":    "Crystal",
	"Zalera":    "Crystal",

	// NA - Dynamis
	"Cuchulainn":    "Dynamis",
	"Golem":         "Dynamis",
	"Halicarnassus": "Dynamis",
	"Kraken":        "Dynamis",
	"Maduin":        "Dynamis",
	"Marilith":      "Dynamis",
	"Rafflesia":     "Dynamis",
	"Seraph":        "Dynamis",

	// EU - Chaos
	"Cerberus":    "Chaos",
	"Louisoix":    "Chaos",
	"Moogle":      "Chaos",
	"Omega":       "Chaos",
	"Phantom":     "Chaos",
	"Ragnarok":    "Chaos",
	"Sagittarius": "Chaos",
	"Spriggan":    "Chaos",

	// EU - Light
	"Alpha":     "Light",
	"Lich":      "Light",
	"Odin":      "Light",
	"Phoenix":   "Light",
	"Raiden":    "Light",
	"Shiva":     "Light",
	"Twintania": "Light",
	"Zodiark":   "Light",

	// JP - Elemental
	"Aegis":     "Elemental",
	"Atomos":    "Elemental",
	"Carbuncle": "Elemental",
	"Garuda":    "Elemental",
	"Gungnir":   "Elemental",
	"Kujata":    "Elemental",
	"Tonberry":  "Elemental",
	"Typhon":    "Elemental",

	// JP - Gaia
	"Alexander": "Gaia",
	"Bahamut":   "Gaia",
	"Durandal":  "Gaia",
	"Fenrir":    "Gaia",
	"Ifrit":     "Gaia",
	"Ridill":    "Gaia",
	"Tiamat":    "Gaia",
	"Ultima":    "Gaia",

	// JP - Mana
	"Anima":        "Mana",
	"Asura":        "Mana",
	"Chocobo":      "Mana",
	"Hades":        "Mana",
	"Ixion":        "Mana",
	"Masamune":     "Mana",
	"Pandaemonium": "Mana",
	"Titan":        "Mana",

	// JP - Meteor
	"Belias":     "Meteor",
	"Mandragora": "Meteor",
	"Ramuh":      "Meteor",
	"Shinryu":    "Meteor",
	"Unicorn":    "Meteor",
	"Valefor":    "Meteor",
	"Yojimbo":    "Meteor",
	"Zeromus":    "Meteor",

	// OCE - Materia
	"Bismarck": "Materia",
	"Ravana":   "Materia",
	"Sephirot": "Materia",
	"Sophia":   "Materia",
	"Zurvan":   "Materia",
}

// WorldDatacenter returns the logical datacenter for a world name, matched
// case-insensitively, or "" when unknown.
func WorldDatacenter(world string) string {
	if dc, ok := worldDatacenters[world]; ok {
		return dc
	}
	for known, dc := range worldDatacenters {
		if strings.EqualFold(known, world) {
			return dc
		}
	}
	return ""
}

// Worlds returns every known world name, sorted.
func Worlds() []string {
	worlds := make([]string, 0, len(worldDatacenters))
	for world := range worldDatacenters {
		worlds = append(worlds, world)
	}
	sort.Strings(worlds)
	return worlds
}

// Datacenters returns every known logical datacenter name, sorted.
func Datacenters() []string {
	dcs := make([]string, 0, len(datacenterRegion))
	for dc := range datacenterRegion {
		dcs = append(dcs, dc)
	}
	sort.Strings(dcs)
	return dcs
}

// DatacentersForRegion returns sorted datacenter names belonging to the given
// region, matched case-insensitively. Returns nil for empty or unknown
// regions.
func DatacentersForRegion(region string) []string {
	if region == "" {
		return nil
	}
	var dcs []string
	for dc, dcRegion := range datacenterRegion {
		if strings.EqualFold(dcRegion, region) {
			dcs = append(dcs, dc)
		}
	}
	sort.Strings(dcs)
	return dcs
}

// WorldsForDatacenter returns sorted world names belonging to the given
// datacenter, matched case-insensitively. Returns nil for empty or unknown
// datacenters.
func WorldsForDatacenter(dc string) []string {
	if dc == "" {
		return nil
	}
	var worlds []string
	for world, worldDC := range worldDatacenters {
		if strings.EqualFold(worldDC, dc) {
			worlds = append(worlds, world)
		}
	}
	sort.Strings(worlds)
	return worlds
}

// WorldsForRegion returns sorted world names belonging to the given region
// through the world hierarchy, matched case-insensitively. Returns nil for
// empty or unknown regions.
func WorldsForRegion(region string) []string {
	if region == "" {
		return nil
	}
	var worlds []string
	for world, dc := range worldDatacenters {
		if strings.EqualFold(datacenterRegion[dc], region) {
			worlds = append(worlds, world)
		}
	}
	sort.Strings(worlds)
	return worlds
}

// CanonicalWorld resolves a world name to its canonical spelling,
// case-insensitively; unknown names pass through unchanged.
func CanonicalWorld(world string) string {
	if _, ok := worldDatacenters[world]; ok {
		return world
	}
	for known := range worldDatacenters {
		if strings.EqualFold(known, world) {
			return known
		}
	}
	return world
}
