package ui

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	mockqueue "github.com/mihaiflorentin88/ffxiv-census/mock/queue"
	mockrepo "github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// windowedStatsRepository serves the test snapshot as if it had been built
// with a different activity window from the running service's, so pages
// must read the window from the snapshot.
type windowedStatsRepository struct {
	testStatsRepository
	activityDays int
}

func (r *windowedStatsRepository) LoadCurrent(ctx context.Context) (*contract.UIStatsSnapshot, error) {
	snapshot, err := r.testStatsRepository.LoadCurrent(ctx)
	if err != nil {
		return nil, err
	}
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-time.Duration(r.activityDays) * 24 * time.Hour)
	return snapshot, nil
}

// staleWindowCopy matches copy that hard-codes a 30-day window.
var staleWindowCopy = regexp.MustCompile(`30 days|30-day|\(30d\)|\b30d\b`)

// newCharacterCopy matches lines about new characters, whose window is not
// the activity window: the new-character timeline panels read "Last 30 days".
var newCharacterCopy = regexp.MustCompile(`(?i)new|chocobo|growth|previous|timeline|panel-meta">Last 30 days`)

// windowedFixtureMux serves a hand-built snapshot as if it had been built
// with an activity window of days.
func windowedFixtureMux(t *testing.T, snapshot *contract.UIStatsSnapshot, days int) *http.ServeMux {
	t.Helper()
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-time.Duration(days) * 24 * time.Hour)
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Hour, 24*time.Hour)
	mux := http.NewServeMux()
	NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
	return mux
}

// TestActivityCopyStatesTheSnapshotWindow renders every page that talks
// about active players from a snapshot built with a 45-day activity window,
// while the service runs the default 90 days. Each page must state the
// snapshot's window, and no activity copy may keep a 30-day literal.
func TestActivityCopyStatesTheSnapshotWindow(t *testing.T) {
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	seedWorldsCharacters(t, rig)
	stats := census.NewUIStatsService(&windowedStatsRepository{testStatsRepository: testStatsRepository{svc: rig.svc}, activityDays: 45}, time.Nanosecond, time.Hour)
	seeded := http.NewServeMux()
	NewUIController(rig.svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(seeded)

	version := contract.UIStatsSchemaVersion
	pages := []struct {
		path string
		mux  *http.ServeMux
	}{
		{"/", seeded},
		{"/ui/races", seeded},
		{"/ui/worlds", seeded},
		{"/ui/worlds?rank=active", seeded},
		{"/ui/worlds/Balmung", seeded},
		{"/ui/partials/world-breakdown?region=NA", seeded},
		// Methodology reads the window of the snapshot already loaded.
		{"/ui/methodology", seeded},
		{"/ui/grand-companies", windowedFixtureMux(t, grandCompanySnapshot(version), 45)},
		{"/ui/rankings?metric=activity", windowedFixtureMux(t, rankingsSnapshot(version), 45)},
		{"/ui/rankings/free-companies?measure=active", windowedFixtureMux(t, rankingsSnapshot(version), 45)},
		{"/ui/privacy", windowedFixtureMux(t, privacyPageSnapshot(version), 45)},
	}
	for _, page := range pages {
		t.Run(page.path, func(t *testing.T) {
			rec := serveMuxGet(t, page.mux, page.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status %d", page.path, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "45 days") {
				t.Errorf("page does not state the snapshot's 45-day activity window")
			}
			if strings.Contains(body, "90 days") {
				t.Errorf("page states the service's 90-day window instead of the snapshot's")
			}
			for _, line := range strings.Split(body, "\n") {
				if staleWindowCopy.MatchString(line) && !newCharacterCopy.MatchString(line) {
					t.Errorf("copy keeps a 30-day window: %s", strings.TrimSpace(line))
				}
			}
		})
	}
}
