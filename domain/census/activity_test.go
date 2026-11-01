package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestActiveSinceCountsAnAchievementOrAnObservedChange(t *testing.T) {
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := since.Add(d); return &v }
	for _, tc := range []struct {
		name            string
		latest, changed *time.Time
		want            bool
	}{
		{"nothing known", nil, nil, false},
		{"achievement in the window", at(time.Hour), nil, true},
		{"achievement at the start of the window", at(0), nil, true},
		{"change in the window, private character", nil, at(time.Hour), true},
		{"both before the window", at(-time.Hour), at(-time.Minute), false},
		{"old achievement, recent change", at(-1000 * time.Hour), at(time.Hour), true},
	} {
		rec := contract.CharacterRecord{LatestAchievementAt: tc.latest, LastChangeAt: tc.changed}
		if got := ActiveSince(rec, since); got != tc.want {
			t.Errorf("%s: ActiveSince = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServiceIsActiveUsesTheActivityWindow(t *testing.T) {
	svc := &Service{activityWindow: 90 * 24 * time.Hour}
	recent := time.Now().Add(-10 * 24 * time.Hour)
	old := time.Now().Add(-100 * 24 * time.Hour)
	if !svc.IsActive(contract.CharacterRecord{LastChangeAt: &recent}) {
		t.Error("a change 10 days ago is not active in a 90-day window")
	}
	if svc.IsActive(contract.CharacterRecord{LatestAchievementAt: &old, LastChangeAt: &old}) {
		t.Error("activity 100 days ago is active in a 90-day window")
	}
}
