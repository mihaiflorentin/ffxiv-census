package census

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestWindowDays(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want int
	}{
		{name: "whole days", in: 90 * 24 * time.Hour, want: 90},
		{name: "a few seconds short rounds up", in: 90*24*time.Hour - 3*time.Second, want: 90},
		{name: "a few seconds over rounds down", in: 30*24*time.Hour + 3*time.Second, want: 30},
		{name: "zero", in: 0, want: 0},
		{name: "negative", in: -time.Hour, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WindowDays(tt.in); got != tt.want {
				t.Fatalf("WindowDays(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestSnapshotActivityWindowDays(t *testing.T) {
	generated := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	snapshot := &contract.UIStatsSnapshot{GeneratedAt: generated, ActivitySince: generated.Add(-90 * 24 * time.Hour)}
	if got := SnapshotActivityWindowDays(snapshot); got != 90 {
		t.Fatalf("SnapshotActivityWindowDays = %d, want 90", got)
	}
	if got := SnapshotActivityWindowDays(nil); got != 0 {
		t.Fatalf("SnapshotActivityWindowDays(nil) = %d, want 0", got)
	}
}

// The new-characters windows end at the start of the generation UTC day
// (exclusive): the current window is the W complete days before it, the
// previous window the W days before that.
func TestNewCharactersBoundsEndAtTheGenerationDay(t *testing.T) {
	got := NewCharactersWindowBounds(time.Date(2026, 10, 24, 10, 30, 0, 0, time.FixedZone("EEST", 3*3600)), 90)
	want := NewCharactersBounds{
		Days:          90,
		End:           time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
		CurrentStart:  time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC),
		PreviousStart: time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC),
	}
	if got != want {
		t.Fatalf("NewCharactersWindowBounds = %+v, want %+v", got, want)
	}
	if !got.InCurrent(time.Date(2026, 10, 23, 23, 59, 0, 0, time.UTC)) || got.InCurrent(got.End) || !got.InCurrent(got.CurrentStart) {
		t.Fatal("InCurrent must cover [CurrentStart, End)")
	}
	if !got.InPrevious(got.PreviousStart) || got.InPrevious(got.CurrentStart) || got.InPrevious(got.PreviousStart.Add(-time.Second)) {
		t.Fatal("InPrevious must cover [PreviousStart, CurrentStart)")
	}
}

// A snapshot's bounds use the window it was built with: the recorded window,
// or the fixed 30 days of older snapshots.
func TestSnapshotNewCharactersBounds(t *testing.T) {
	generated := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	v4 := &contract.UIStatsSnapshot{GeneratedAt: generated, NewCharactersWindowDays: 10}
	if got := SnapshotNewCharactersBounds(v4); got.Days != 10 || !got.CurrentStart.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("SnapshotNewCharactersBounds(v4) = %+v", got)
	}
	v3 := &contract.UIStatsSnapshot{GeneratedAt: generated}
	if got := SnapshotNewCharactersBounds(v3); got.Days != 30 || !got.PreviousStart.Equal(time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("SnapshotNewCharactersBounds(v3) = %+v", got)
	}
}
