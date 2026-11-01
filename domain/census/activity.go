package census

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ActiveSince is the census definition of an active character: its latest
// public achievement or its last observed change (a census that found a job
// levelled or unlocked, another current job, name, world, Free Company or
// Grand Company, or a newer achievement) is at or after since. Every
// "active" figure, filter and flag uses it; the SQL counts mirror it as
// latest_achievement_at >= since OR last_change_at >= since.
func ActiveSince(rec contract.CharacterRecord, since time.Time) bool {
	return atOrAfter(rec.LatestAchievementAt, since) || atOrAfter(rec.LastChangeAt, since)
}

func atOrAfter(t *time.Time, since time.Time) bool {
	return t != nil && !t.Before(since)
}
