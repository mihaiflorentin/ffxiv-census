package lodestone

import (
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// Every job and class name the Lodestone client maps must count towards a job
// of the census taxonomy, and a mapped job must carry the taxonomy's name.
func TestLodestoneJobIDsAreInTheCensusTaxonomy(t *testing.T) {
	jobNames := 0
	for name, id := range lodestoneJobIDs {
		folded, ok := census.FoldJobID(id)
		if !ok {
			t.Errorf("%s (%d) does not fold into a census job", name, id)
			continue
		}
		if folded == id {
			jobNames++
			if job, _ := census.JobByID(id); job.Name != name {
				t.Errorf("Lodestone job %q (%d) is %q in the taxonomy", name, id, job.Name)
			}
		}
	}
	if want := len(census.Jobs()); jobNames != want {
		t.Errorf("Lodestone maps %d job names, taxonomy has %d jobs", jobNames, want)
	}
}
