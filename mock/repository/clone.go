package repository

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Deep-copy helpers so fakes never alias the pointer fields they store/return
// (matching the real SQLite impls, which scan fresh values each call).

func cloneString(s *string) *string {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func cloneUint32(v *uint32) *uint32 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

func cloneCharacter(rec contract.CharacterRecord) contract.CharacterRecord {
	rec.FreeCompanyID = cloneString(rec.FreeCompanyID)
	rec.FreeCompanyName = cloneString(rec.FreeCompanyName)
	rec.LatestAchievementID = cloneUint32(rec.LatestAchievementID)
	rec.LatestAchievementAt = cloneTime(rec.LatestAchievementAt)
	rec.LastChangeAt = cloneTime(rec.LastChangeAt)
	rec.AchievementsCheckedAt = cloneTime(rec.AchievementsCheckedAt)
	rec.LastCensusAt = cloneTime(rec.LastCensusAt)
	rec.DeletedAt = cloneTime(rec.DeletedAt)
	return rec
}

func cloneMilestone(m contract.MilestoneAchievement) contract.MilestoneAchievement {
	m.Expansion = cloneString(m.Expansion)
	return m
}

// atOrAfter reports whether t is set and not before since.
func atOrAfter(t *time.Time, since time.Time) bool {
	return t != nil && !t.Before(since)
}
