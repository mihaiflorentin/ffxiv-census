package contract

import "time"

// StatsJobsSection is the job section of the UI statistics snapshot: per
// world, how many characters have levelled each job and how many have it at
// its level cap, summed from the characters' job bitmasks by the nightly full
// refresh. It is stored at world granularity only; region, datacenter and
// global figures are rolled up in the census domain. The incremental refresh
// carries it forward unchanged, with its BuiltAt.
type StatsJobsSection struct {
	// BuiltAt is the time of the full refresh that produced the section.
	BuiltAt time.Time `json:"built_at"`
	// JobIDs are the official job ids, in Armoury order: the columns of every
	// world's Levelled and AtCap.
	JobIDs []int            `json:"job_ids"`
	Worlds []WorldJobCounts `json:"worlds"`
}

// WorldJobCounts is one world's row of the job section.
type WorldJobCounts struct {
	World string `json:"world"`
	// Characters is the world's live characters when the section was built;
	// Covered is how many of them have their job bitmasks computed.
	Characters int64   `json:"characters"`
	Covered    int64   `json:"covered"`
	Levelled   []int64 `json:"levelled"`
	AtCap      []int64 `json:"at_cap"`
}
