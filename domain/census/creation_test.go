package census

import (
	"reflect"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestCreationAnchors(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d) }
	exact := CreationAnchor{ID: 1_000_000, At: day(100)}
	tests := []struct {
		name  string
		bands []CreationBand
		want  []CreationAnchor
	}{
		{
			name:  "each band anchors its midpoint at its early date, then the exact anchor",
			bands: []CreationBand{{FirstID: 0, EndID: 100_000, Samples: 50, Early: day(0)}, {FirstID: 100_000, EndID: 200_000, Samples: 40, Early: day(10)}},
			want:  []CreationAnchor{{ID: 50_000, At: day(0)}, {ID: 150_000, At: day(10)}, exact},
		},
		{
			name:  "a band with too few samples is left out",
			bands: []CreationBand{{FirstID: 0, EndID: 100_000, Samples: 50, Early: day(0)}, {FirstID: 100_000, EndID: 200_000, Samples: 29, Early: day(3)}, {FirstID: 200_000, EndID: 300_000, Samples: 30, Early: day(20)}},
			want:  []CreationAnchor{{ID: 50_000, At: day(0)}, {ID: 250_000, At: day(20)}, exact},
		},
		{
			name:  "an earlier date after a later one is raised to it, so the curve never goes back",
			bands: []CreationBand{{FirstID: 0, EndID: 100_000, Samples: 50, Early: day(10)}, {FirstID: 100_000, EndID: 200_000, Samples: 50, Early: day(8)}},
			want:  []CreationAnchor{{ID: 50_000, At: day(10)}, {ID: 150_000, At: day(10)}, exact},
		},
		{
			name:  "a band reaching the exact cut is left out, and the bands are sorted",
			bands: []CreationBand{{FirstID: 950_000, EndID: 1_050_000, Samples: 50, Early: day(99)}, {FirstID: 0, EndID: 100_000, Samples: 50, Early: day(0)}},
			want:  []CreationAnchor{{ID: 50_000, At: day(0)}, exact},
		},
		{
			name:  "a band dated after the exact cut is held at it",
			bands: []CreationBand{{FirstID: 0, EndID: 100_000, Samples: 50, Early: day(120)}},
			want:  []CreationAnchor{{ID: 50_000, At: day(100)}, exact},
		},
		{
			name: "without bands only the exact anchor is left",
			want: []CreationAnchor{exact},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CreationAnchors(tt.bands, 30, exact); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("CreationAnchors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreationBoundaries(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d) }
	tests := []struct {
		name      string
		anchors   []CreationAnchor
		from      time.Time
		until     time.Time
		wantFirst time.Time
		want      []int64
	}{
		{
			name:      "days between two anchors split the IDs evenly",
			anchors:   []CreationAnchor{{ID: 1000, At: day(0)}, {ID: 2000, At: day(10)}},
			from:      day(7),
			until:     day(10),
			wantFirst: day(7),
			want:      []int64{1700, 1800, 1900, 2000},
		},
		{
			name:      "a date between anchors interpolates within its segment",
			anchors:   []CreationAnchor{{ID: 1000, At: day(0)}, {ID: 2000, At: day(10)}, {ID: 2400, At: day(12)}},
			from:      day(9),
			until:     day(12),
			wantFirst: day(9),
			want:      []int64{1900, 2000, 2200, 2400},
		},
		{
			name:      "days before the first anchor are not covered",
			anchors:   []CreationAnchor{{ID: 1000, At: day(2)}, {ID: 2000, At: day(4)}},
			from:      day(0),
			until:     day(4),
			wantFirst: day(2),
			want:      []int64{1000, 1500, 2000},
		},
		{
			name:      "a flat stretch starts the day at its first ID",
			anchors:   []CreationAnchor{{ID: 1000, At: day(0)}, {ID: 1500, At: day(1)}, {ID: 1800, At: day(1)}, {ID: 2000, At: day(2)}},
			from:      day(0),
			until:     day(2),
			wantFirst: day(0),
			want:      []int64{1000, 1500, 2000},
		},
		{
			name:    "one anchor covers no day",
			anchors: []CreationAnchor{{ID: 1000, At: day(0)}},
			from:    day(0),
			until:   day(0),
		},
		{
			name:    "a curve that starts after until covers no day",
			anchors: []CreationAnchor{{ID: 1000, At: day(5)}, {ID: 2000, At: day(6)}},
			from:    day(0),
			until:   day(4),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, got := CreationBoundaries(tt.anchors, tt.from, tt.until)
			if !reflect.DeepEqual(got, tt.want) || (len(tt.want) > 0 && !first.Equal(tt.wantFirst)) {
				t.Fatalf("CreationBoundaries = %v, %v; want %v, %v", first, got, tt.wantFirst, tt.want)
			}
		})
	}
}

func TestValidateUIStatsSnapshotRejectsInvalidCreatedFigures(t *testing.T) {
	tests := []struct {
		name   string
		change func(*contract.UIStatsSnapshot)
	}{
		{name: "negative day", change: func(s *contract.UIStatsSnapshot) {
			s.Created = []contract.ScopedDailyCount{{Day: "2026-10-01", Count: -1}}
		}},
		{name: "unparseable day", change: func(s *contract.UIStatsSnapshot) {
			s.Created = []contract.ScopedDailyCount{{Day: "Oct 1", Count: 1}}
		}},
		{name: "duplicate day", change: func(s *contract.UIStatsSnapshot) {
			s.Created = []contract.ScopedDailyCount{{Day: "2026-10-01", Count: 1}, {Day: "2026-10-01", Count: 2}}
		}},
		{name: "unparseable exact day", change: func(s *contract.UIStatsSnapshot) { s.CreatedExactFrom = "Oct 21" }},
		{name: "negative group total", change: func(s *contract.UIStatsSnapshot) { s.Groups[0].CreatedPrevious = -1 }},
		{name: "negative group day", change: func(s *contract.UIStatsSnapshot) { s.Groups[0].CreatedDaily = []int64{1, -1} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validUIStatsSnapshot()
			tt.change(s)
			if err := ValidateUIStatsSnapshot(s); err == nil {
				t.Fatal("ValidateUIStatsSnapshot accepted invalid created figures")
			}
		})
	}
	s := validUIStatsSnapshot()
	s.Created = []contract.ScopedDailyCount{{Day: "2026-10-01", Count: 4}}
	s.CreatedExactFrom = "2026-10-21"
	if err := ValidateUIStatsSnapshot(s); err != nil {
		t.Fatalf("valid created figures rejected: %v", err)
	}
}
