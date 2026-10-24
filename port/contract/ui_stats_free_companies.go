package contract

import "time"

// StatsFreeCompanies is the Free Company read model: per world, the Free
// Companies on that world's two boards (the top entries by members seen and
// by active members), built by the nightly full refresh in one streaming pass
// over live Free Company members. It is stored in its own snapshot row, apart
// from UIStatsSnapshot, so the main snapshot reloaded every minute stays
// small; the incremental refresh never touches it. Datacenter, region and
// global boards are merged from the world lists in the census domain, which
// is exact because every Free Company belongs to one world.
type StatsFreeCompanies struct {
	// BuiltAt is the time of the full refresh that produced the boards; it
	// equals UIStatsSnapshot.FreeCompaniesBuiltAt of that refresh.
	BuiltAt time.Time `json:"built_at"`
	// ActivitySince is the activity cutoff the active counts used.
	ActivitySince time.Time `json:"activity_since"`
	// Worlds are sorted by name.
	Worlds []WorldFreeCompanies `json:"worlds"`
}

// WorldFreeCompanies is one world's Free Companies: the union of its top
// entries by members and by active members, each company once.
type WorldFreeCompanies struct {
	World     string             `json:"world"`
	Companies []FreeCompanyCount `json:"companies"`
}

// FreeCompanyCount is one Free Company's counts. Members are the live
// characters the census has seen in it, not the official roster size; Active
// are those with a public achievement inside the activity window.
type FreeCompanyCount struct {
	// ID is the Lodestone Free Company id.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Members and Active as above.
	Members int64 `json:"members"`
	Active  int64 `json:"active,omitempty"`
}
