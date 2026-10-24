package ui

import (
	"testing"
	"time"
)

func TestWindowCopy(t *testing.T) {
	generated := time.Date(2026, 10, 24, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		window            Window
		wantLong, wantShr string
	}{
		{name: "90 days from a snapshot", window: windowBetween(generated.Add(-90*24*time.Hour), generated), wantLong: "90 days", wantShr: "90d"},
		{name: "a few seconds short still reads 45 days", window: windowBetween(generated.Add(-45*24*time.Hour+2*time.Second), generated), wantLong: "45 days", wantShr: "45d"},
		{name: "one day is singular", window: Window{Days: 1}, wantLong: "1 day", wantShr: "1d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.window.Long(); got != tt.wantLong {
				t.Errorf("Long() = %q, want %q", got, tt.wantLong)
			}
			if got := tt.window.Short(); got != tt.wantShr {
				t.Errorf("Short() = %q, want %q", got, tt.wantShr)
			}
		})
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{0, "0"},
		{5, "5"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
		{1000000000, "1,000,000,000"},
		{-1234, "-1,234"},
	}

	for _, tt := range tests {
		got := formatNumber(tt.input)
		if got != tt.expected {
			t.Errorf("formatNumber(%d) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	tests := []struct {
		num      int64
		denom    int64
		expected string
	}{
		{0, 0, "0.0%"},
		{0, 100, "0.0%"},
		{25, 100, "25.0%"},
		{1, 3, "33.3%"},
		{2, 3, "66.7%"},
		{100, 100, "100.0%"},
	}

	for _, tt := range tests {
		got := formatPercent(tt.num, tt.denom)
		if got != tt.expected {
			t.Errorf("formatPercent(%d, %d) = %q; want %q", tt.num, tt.denom, got, tt.expected)
		}
	}
}

func TestFormatDate(t *testing.T) {
	tm := time.Date(2026, 8, 16, 15, 4, 5, 0, time.UTC)
	if got := formatDate(&tm); got != "2026-08-16" {
		t.Errorf("formatDate(&tm) = %q; want %q", got, "2026-08-16")
	}
	if got := formatDate(tm); got != "2026-08-16" {
		t.Errorf("formatDate(tm) = %q; want %q", got, "2026-08-16")
	}
	if got := formatDate(nil); got != "-" {
		t.Errorf("formatDate(nil) = %q; want %q", got, "-")
	}
}

func TestJobCategory(t *testing.T) {
	tests := []struct {
		jobName  string
		expected string
	}{
		{"Paladin", "tank"},
		{"Gladiator", "tank"},
		{"Warrior", "tank"},
		{"Dark Knight", "tank"},
		{"Gunbreaker", "tank"},
		{"White Mage", "healer"},
		{"Conjurer", "healer"},
		{"Scholar", "healer"},
		{"Astrologian", "healer"},
		{"Sage", "healer"},
		{"Monk", "melee"},
		{"Pugilist", "melee"},
		{"Dragoon", "melee"},
		{"Lancer", "melee"},
		{"Ninja", "melee"},
		{"Rogue", "melee"},
		{"Samurai", "melee"},
		{"Reaper", "melee"},
		{"Viper", "melee"},
		{"Bard", "phys_ranged"},
		{"Archer", "phys_ranged"},
		{"Machinist", "phys_ranged"},
		{"Dancer", "phys_ranged"},
		{"Black Mage", "magic_ranged"},
		{"Thaumaturge", "magic_ranged"},
		{"Summoner", "magic_ranged"},
		{"Arcanist", "magic_ranged"},
		{"Red Mage", "magic_ranged"},
		{"Pictomancer", "magic_ranged"},
		{"Blue Mage", "magic_ranged"},
		{"Carpenter", "crafter"},
		{"Blacksmith", "crafter"},
		{"Miner", "gatherer"},
		{"Botanist", "gatherer"},
		{"Fisher", "gatherer"},
		{"UnknownJob", "other"},
	}

	for _, tt := range tests {
		got := jobCategory(tt.jobName)
		if got != tt.expected {
			t.Errorf("jobCategory(%q) = %q; want %q", tt.jobName, got, tt.expected)
		}
	}
}

func TestRoleColor(t *testing.T) {
	tests := []struct {
		role     string
		expected string
	}{
		{"tank", "role-tank"},
		{"healer", "role-healer"},
		{"melee", "role-melee"},
		{"phys_ranged", "role-phys-ranged"},
		{"magic_ranged", "role-magic-ranged"},
		{"crafter", "role-crafter"},
		{"gatherer", "role-gatherer"},
		{"other", "role-other"},
	}

	for _, tt := range tests {
		got := roleColor(tt.role)
		if got != tt.expected {
			t.Errorf("roleColor(%q) = %q; want %q", tt.role, got, tt.expected)
		}
	}
}
