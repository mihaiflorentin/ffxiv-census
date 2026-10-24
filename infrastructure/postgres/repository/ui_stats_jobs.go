package repository

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The job section of the full refresh. The fine-grain pass over characters
// also sums the two job bitmasks (one levelled and one at-cap count per job
// bit) and counts the characters whose masks are set. It never reads the
// job table. The section is kept per world only.

// extraColumnScanner scans the columns a fine-grain query carries after the fineCounts columns:
// dest returns fresh scan targets for one row and add folds the scanned row in.
type extraColumnScanner interface {
	dest() []any
	add(key fineKey, counts fineCounts)
}

// jobFineColumns are the job-section columns of the full refresh's
// fine-grain query, in jobAccumulator.dest order.
var jobFineColumns = func() string {
	cols := []string{
		"COUNT(*) FILTER (WHERE jobs_levelled_mask IS NOT NULL) AS jobs_covered",
	}
	for _, j := range census.Jobs() {
		bit := fmt.Sprintf("(1::bigint << %d)", j.ID)
		cols = append(cols,
			fmt.Sprintf("COUNT(*) FILTER (WHERE jobs_levelled_mask & %s <> 0) AS levelled_%d", bit, j.ID),
			fmt.Sprintf("COUNT(*) FILTER (WHERE jobs_at_cap_mask & %s <> 0) AS at_cap_%d", bit, j.ID))
	}
	return strings.Join(cols, ",\n\t\t       ")
}()

// jobAccumulator sums the job columns of fine-grain rows per world.
type jobAccumulator struct {
	row    []int64
	worlds map[string]*contract.WorldJobCounts
}

func newJobAccumulator() *jobAccumulator {
	return &jobAccumulator{row: make([]int64, 1+2*len(census.Jobs())), worlds: map[string]*contract.WorldJobCounts{}}
}

func (a *jobAccumulator) dest() []any {
	out := make([]any, len(a.row))
	for i := range a.row {
		a.row[i] = 0
		out[i] = &a.row[i]
	}
	return out
}

func (a *jobAccumulator) add(key fineKey, counts fineCounts) {
	if key.World == "" {
		return
	}
	w := a.worlds[key.World]
	if w == nil {
		n := len(census.Jobs())
		w = &contract.WorldJobCounts{World: key.World, Levelled: make([]int64, n), AtCap: make([]int64, n)}
		a.worlds[key.World] = w
	}
	w.Characters += counts.Total
	w.Covered += a.row[0]
	for i := range w.Levelled {
		w.Levelled[i] += a.row[1+2*i]
		w.AtCap[i] += a.row[2+2*i]
	}
}

// section returns the job section built at builtAt, worlds sorted by name.
func (a *jobAccumulator) section(builtAt time.Time) *contract.StatsJobsSection {
	s := &contract.StatsJobsSection{BuiltAt: builtAt, Worlds: []contract.WorldJobCounts{}}
	for _, j := range census.Jobs() {
		s.JobIDs = append(s.JobIDs, int(j.ID))
	}
	for _, w := range a.worlds {
		s.Worlds = append(s.Worlds, *w)
	}
	sort.Slice(s.Worlds, func(i, j int) bool { return s.Worlds[i].World < s.Worlds[j].World })
	return s
}
