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
	"golang.org/x/net/html/atom"
)

// windowedStatsRepository serves the test snapshot as if it had been built
// with different activity and new-characters windows from the running
// service's, so pages must read the windows from the snapshot.
type windowedStatsRepository struct {
	testStatsRepository
	activityDays      int
	newCharactersDays int
}

func (r *windowedStatsRepository) LoadCurrent(ctx context.Context) (*contract.UIStatsSnapshot, error) {
	snapshot, err := r.testStatsRepository.LoadCurrent(ctx)
	if err != nil {
		return nil, err
	}
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-time.Duration(r.activityDays) * 24 * time.Hour)
	snapshot.NewCharactersWindowDays = r.newCharactersDays
	return snapshot, nil
}

// staleWindowCopy matches copy that hard-codes a 30-day window.
var staleWindowCopy = regexp.MustCompile(`30 days|30-day|\(30d\)|\b30d\b`)

// testNewCharactersDays is the new-characters window the windowed fixtures
// were built with, unlike the activity window and the service's 90 days.
const testNewCharactersDays = 60

// windowedFixtureMux serves a hand-built snapshot as if it had been built
// with an activity window of days and a new-characters window of
// testNewCharactersDays.
func windowedFixtureMux(t *testing.T, snapshot *contract.UIStatsSnapshot, days int) *http.ServeMux {
	t.Helper()
	snapshot.ActivitySince = snapshot.GeneratedAt.Add(-time.Duration(days) * 24 * time.Hour)
	snapshot.NewCharactersWindowDays = testNewCharactersDays
	svc := census.NewService(mockrepo.NewCharacterFake(), mockrepo.NewAchievementFake(), mockrepo.NewCensusRunFake())
	stats := census.NewUIStatsService(mockrepo.NewUIStatsFake(snapshot), time.Hour, 24*time.Hour)
	mux := http.NewServeMux()
	NewUIController(svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(mux)
	return mux
}

// windowedSeededMux serves the seeded test rig as if its snapshot had been
// built with a 45-day activity window and a testNewCharactersDays
// new-characters window.
func windowedSeededMux(t *testing.T) *http.ServeMux {
	t.Helper()
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	seedWorldsCharacters(t, rig)
	stats := census.NewUIStatsService(&windowedStatsRepository{testStatsRepository: testStatsRepository{svc: rig.svc}, activityDays: 45, newCharactersDays: testNewCharactersDays}, time.Nanosecond, time.Hour)
	seeded := http.NewServeMux()
	NewUIController(rig.svc, mockqueue.NewFake(), stats, testBaseURL).RegisterRoutes(seeded)
	return seeded
}

// TestActivityCopyStatesTheSnapshotWindow renders every page that talks
// about active players from a snapshot built with a 45-day activity window,
// while the service runs the default 90 days. Each page must state the
// snapshot's window, and no copy may keep a 30-day literal.
func TestActivityCopyStatesTheSnapshotWindow(t *testing.T) {
	seeded := windowedSeededMux(t)

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
				if staleWindowCopy.MatchString(line) {
					t.Errorf("copy keeps a 30-day window: %s", strings.TrimSpace(line))
				}
			}
		})
	}
}

// TestNewCharacterCopyStatesTheSnapshotWindow renders every page that talks
// about new characters from a snapshot built with a 60-day new-characters
// window, while the service runs the default 90 days. Each page must state
// the snapshot's window, and no copy may keep a 30-day literal.
func TestNewCharacterCopyStatesTheSnapshotWindow(t *testing.T) {
	seeded := windowedSeededMux(t)
	pages := []struct {
		path string
		mux  *http.ServeMux
		want []string
	}{
		// The tile, its trend sentence and the timeline title and summary.
		{"/", seeded, []string{"New characters, last 60 days", "on the previous 60 days", "new characters in the last 60 days", "Last 60 days"}},
		{"/ui/races", seeded, []string{"New characters, last 60 days"}},
		{"/ui/worlds", seeded, []string{"New characters, last 60 days", "New, 60 days"}},
		{"/ui/partials/world-breakdown?region=NA", seeded, []string{"New, 60 days"}},
		{"/ui/worlds/Balmung", seeded, []string{"New characters, last 60 days"}},
		{"/ui/methodology", seeded, []string{"in the last 60 days", "the 60 days before"}},
		{"/ui/rankings?metric=growth", windowedFixtureMux(t, rankingsSnapshot(contract.UIStatsSchemaVersion), 45), []string{"New characters in the last 60 days for every 1,000 characters"}},
		{"/ui/rankings?metric=growth-count", windowedFixtureMux(t, rankingsSnapshot(contract.UIStatsSchemaVersion), 45), []string{"in the last 60 days"}},
	}
	for _, page := range pages {
		t.Run(page.path, func(t *testing.T) {
			rec := serveMuxGet(t, page.mux, page.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status %d", page.path, rec.Code)
			}
			body := rec.Body.String()
			for _, want := range page.want {
				if !strings.Contains(body, want) {
					t.Errorf("page does not state the snapshot's new-characters window: missing %q", want)
				}
			}
			if strings.Contains(body, "90 days") {
				t.Errorf("page states the service's 90-day window instead of the snapshot's")
			}
			for _, line := range strings.Split(body, "\n") {
				if staleWindowCopy.MatchString(line) {
					t.Errorf("copy keeps a 30-day window: %s", strings.TrimSpace(line))
				}
			}
		})
	}
}

// TestPageDescriptionsStateTheNewCharactersWindow checks the meta and Open
// Graph descriptions that mention new characters name the window.
func TestPageDescriptionsStateTheNewCharactersWindow(t *testing.T) {
	seeded := windowedSeededMux(t)
	for _, path := range []string{"/", "/ui/worlds/Balmung"} {
		body := serveMuxGet(t, seeded, path).Body.String()
		doc := parseHTML(t, body)
		var description string
		for _, meta := range findAll(doc, byAtom(atom.Meta)) {
			if name, _ := attr(meta, "name"); name == "description" {
				description, _ = attr(meta, "content")
			}
		}
		if !strings.Contains(description, "new characters in the last 60 days") {
			t.Errorf("%s description = %q, want it to name the 60-day new-characters window", path, description)
		}
	}
}
