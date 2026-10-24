package census

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The Armoury: every job's levelled and at-cap counts for a scope, grouped
// by role, rolled up from the snapshot's per-world job section through the
// world hierarchy.

// ArmouryMeasure selects which count the Armoury shows.
type ArmouryMeasure string

const (
	// MeasureLevelled counts characters with the job at level 1 or more.
	MeasureLevelled ArmouryMeasure = "levelled"
	// MeasureAtCap counts characters with the job at its own level cap.
	MeasureAtCap ArmouryMeasure = "at-cap"
)

// ArmouryJob is one job's figures within a selection.
type ArmouryJob struct {
	Job
	Levelled, AtCap int64
	// covered is the selection's covered characters (the shares' base);
	// leadLevelled and leadAtCap are the measure's highest counts.
	covered, leadLevelled, leadAtCap int64
}

// Count returns the job's count for a measure.
func (j ArmouryJob) Count(m ArmouryMeasure) int64 {
	if m == MeasureAtCap {
		return j.AtCap
	}
	return j.Levelled
}

// Share is the job's count over the characters the figures cover.
func (j ArmouryJob) Share(m ArmouryMeasure) float64 {
	return ratio(j.Count(m), j.covered)
}

// Intensity is the job's count relative to the most common job of the
// measure (1 for the leader), for the tile fill.
func (j ArmouryJob) Intensity(m ArmouryMeasure) float64 {
	lead := j.leadLevelled
	if m == MeasureAtCap {
		lead = j.leadAtCap
	}
	return ratio(j.Count(m), lead)
}

// ArmouryRole is one role group and its jobs, in the Armoury's order.
type ArmouryRole struct {
	Role
	Jobs []ArmouryJob
}

// RoleBalance counts at-cap combat jobs per party role: a character with two
// tanks at the cap counts twice. The limited jobs (Blue Mage, Beastmaster),
// which cannot join ordinary duties, are left out.
type RoleBalance struct {
	Tank, Healer, DPS int64
}

// Total is the at-cap combat jobs counted.
func (b RoleBalance) Total() int64 { return b.Tank + b.Healer + b.DPS }

// TankShare, HealerShare and DPSShare are each role's share of Total.
func (b RoleBalance) TankShare() float64   { return ratio(b.Tank, b.Total()) }
func (b RoleBalance) HealerShare() float64 { return ratio(b.Healer, b.Total()) }
func (b RoleBalance) DPSShare() float64    { return ratio(b.DPS, b.Total()) }

func (b *RoleBalance) add(job Job, atCap int64) {
	switch {
	case job.Role == RoleLimited:
	case job.Role == RoleTank:
		b.Tank += atCap
	case job.Role == RoleHealer:
		b.Healer += atCap
	case job.Role == RoleMeleeDPS || job.Role == RolePhysicalRangedDPS || job.Role == RoleMagicalRangedDPS:
		b.DPS += atCap
	}
}

// Armoury is the job board of one selection.
type Armoury struct {
	// BuiltAt is when the nightly rebuild produced the job figures.
	BuiltAt time.Time
	// Characters is the selection's characters when the figures were built;
	// Covered is how many of them the job figures include.
	Characters, Covered int64
	Roles               []ArmouryRole
	Balance             RoleBalance
}

// HasData reports whether any covered character is in the selection.
func (a Armoury) HasData() bool { return a.Covered > 0 }

// Coverage is Covered over Characters.
func (a Armoury) Coverage() float64 { return ratio(a.Covered, a.Characters) }

// FullyCovered reports whether the figures include every character.
func (a Armoury) FullyCovered() bool { return a.Covered >= a.Characters }

// MostLevelled returns up to n jobs with the most characters levelled, most
// first; ties keep the Armoury order and jobs nobody levelled are left out.
func (a Armoury) MostLevelled(n int) []ArmouryJob {
	var all []ArmouryJob
	for _, r := range a.Roles {
		for _, j := range r.Jobs {
			if j.Levelled > 0 {
				all = append(all, j)
			}
		}
	}
	sort.SliceStable(all, func(i, k int) bool { return all[i].Levelled > all[k].Levelled })
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// ArmouryFor rolls the job section up to a stats scope. A snapshot without a
// job section yields an empty Armoury (no roles).
func ArmouryFor(snapshot *contract.UIStatsSnapshot, scope contract.StatsScope) Armoury {
	if snapshot == nil || snapshot.Jobs == nil {
		return Armoury{}
	}
	section := snapshot.Jobs
	a := Armoury{BuiltAt: section.BuiltAt}
	levelled := map[uint8]int64{}
	atCap := map[uint8]int64{}

	global := scope == (contract.StatsScope{})
	selected := worldSet(scope)
	for _, w := range section.Worlds {
		if !global {
			if _, ok := selected[w.World]; !ok {
				continue
			}
		}
		a.Characters += w.Characters
		a.Covered += w.Covered
		for i, id := range section.JobIDs {
			if i < len(w.Levelled) {
				levelled[uint8(id)] += w.Levelled[i]
			}
			if i < len(w.AtCap) {
				atCap[uint8(id)] += w.AtCap[i]
			}
		}
	}

	var leadLevelled, leadAtCap int64
	for _, j := range jobs {
		leadLevelled = max(leadLevelled, levelled[j.ID])
		leadAtCap = max(leadAtCap, atCap[j.ID])
	}
	for _, role := range roles {
		ar := ArmouryRole{Role: role}
		for _, j := range jobs {
			if j.Role != role.Code {
				continue
			}
			ar.Jobs = append(ar.Jobs, ArmouryJob{
				Job: j, Levelled: levelled[j.ID], AtCap: atCap[j.ID],
				covered: a.Covered, leadLevelled: leadLevelled, leadAtCap: leadAtCap,
			})
			a.Balance.add(j, atCap[j.ID])
		}
		a.Roles = append(a.Roles, ar)
	}
	return a
}

// validateJobsSection checks a snapshot's job section, when it has one.
func validateJobsSection(s *contract.StatsJobsSection) error {
	if s == nil {
		return nil
	}
	if s.BuiltAt.IsZero() {
		return errors.New("UI statistics job section needs its build time")
	}
	seen := make(map[string]struct{}, len(s.Worlds))
	for _, w := range s.Worlds {
		if _, ok := seen[w.World]; ok || w.World == "" {
			return fmt.Errorf("invalid or duplicate job section world %q", w.World)
		}
		seen[w.World] = struct{}{}
		if len(w.Levelled) != len(s.JobIDs) || len(w.AtCap) != len(s.JobIDs) {
			return fmt.Errorf("job section world %q has the wrong number of jobs", w.World)
		}
		if w.Covered < 0 || w.Covered > w.Characters {
			return fmt.Errorf("job section world %q covers %d of %d characters", w.World, w.Covered, w.Characters)
		}
		for i := range s.JobIDs {
			if w.AtCap[i] < 0 || w.AtCap[i] > w.Levelled[i] || w.Levelled[i] > w.Covered {
				return fmt.Errorf("job section world %q has invalid counts for job %d", w.World, s.JobIDs[i])
			}
		}
	}
	return nil
}

// DatacenterRoleBalance is one datacenter's role balance.
type DatacenterRoleBalance struct {
	Region, Datacenter string
	RoleBalance
}

// RoleBalanceByDatacenter returns the role balance of every datacenter with
// at-cap combat jobs, optionally narrowed to one region, ordered by region
// (census order) then datacenter name.
func RoleBalanceByDatacenter(snapshot *contract.UIStatsSnapshot, region string) []DatacenterRoleBalance {
	if snapshot == nil || snapshot.Jobs == nil {
		return nil
	}
	region = CanonicalRegion(region)
	balances := map[string]*RoleBalance{}
	for _, w := range snapshot.Jobs.Worlds {
		dc := WorldDatacenter(w.World)
		if dc == "" || (region != "" && RegionForDatacenter(dc) != region) {
			continue
		}
		b := balances[dc]
		if b == nil {
			b = &RoleBalance{}
			balances[dc] = b
		}
		for i, id := range snapshot.Jobs.JobIDs {
			if j, ok := jobByID[uint8(id)]; ok && i < len(w.AtCap) {
				b.add(j, w.AtCap[i])
			}
		}
	}
	var out []DatacenterRoleBalance
	for _, r := range regions {
		var dcs []string
		for dc, b := range balances {
			if RegionForDatacenter(dc) == r.Code && b.Total() > 0 {
				dcs = append(dcs, dc)
			}
		}
		sort.Strings(dcs)
		for _, dc := range dcs {
			out = append(out, DatacenterRoleBalance{Region: r.Code, Datacenter: dc, RoleBalance: *balances[dc]})
		}
	}
	return out
}
