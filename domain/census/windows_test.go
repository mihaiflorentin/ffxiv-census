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
