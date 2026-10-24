package census

import (
	"reflect"
	"testing"
)

func TestRaceOrderIsTheGameOrder(t *testing.T) {
	want := []string{"Hyur", "Elezen", "Lalafell", "Miqo'te", "Roegadyn", "Au Ra", "Hrothgar", "Viera"}
	if got := RaceOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RaceOrder() = %v, want %v", got, want)
	}
	RaceOrder()[0] = "Mutated"
	if RaceOrder()[0] != "Hyur" {
		t.Fatal("RaceOrder() must return a copy")
	}
}

func TestRaceRank(t *testing.T) {
	cases := []struct {
		race string
		want int
	}{
		{"Hyur", 1}, {"Miqo'te", 4}, {"Viera", 8}, {"Unknown", 0}, {"", 0}, {"hyur", 0},
	}
	for _, c := range cases {
		if got := RaceRank(c.race); got != c.want {
			t.Errorf("RaceRank(%q) = %d, want %d", c.race, got, c.want)
		}
	}
}

func TestClanRace(t *testing.T) {
	cases := []struct{ clan, want string }{
		{"Midlander", "Hyur"}, {"Highlander", "Hyur"},
		{"Wildwood", "Elezen"}, {"Duskwight", "Elezen"},
		{"Plainsfolk", "Lalafell"}, {"Dunesfolk", "Lalafell"},
		{"Seeker of the Sun", "Miqo'te"}, {"Seekers of the Sun", "Miqo'te"},
		{"Keeper of the Moon", "Miqo'te"}, {"Keepers of the Moon", "Miqo'te"},
		{"Sea Wolf", "Roegadyn"}, {"Sea Wolves", "Roegadyn"}, {"Hellsguard", "Roegadyn"},
		{"Raen", "Au Ra"}, {"Xaela", "Au Ra"},
		{"Helions", "Hrothgar"}, {"The Lost", "Hrothgar"},
		{"Rava", "Viera"}, {"Veena", "Viera"},
		{"Unheard Of", ""},
	}
	for _, c := range cases {
		if got := ClanRace(c.clan); got != c.want {
			t.Errorf("ClanRace(%q) = %q, want %q", c.clan, got, c.want)
		}
	}
}

func TestRegionsInDisplayOrder(t *testing.T) {
	want := []Region{
		{Code: "NA", Name: "North America"},
		{Code: "EU", Name: "Europe"},
		{Code: "JP", Name: "Japan"},
		{Code: "OCE", Name: "Oceania"},
	}
	if got := Regions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Regions() = %v, want %v", got, want)
	}
	Regions()[0].Code = "XX"
	if Regions()[0].Code != "NA" {
		t.Fatal("Regions() must return a copy")
	}
}

func TestRegionNameAndRank(t *testing.T) {
	cases := []struct {
		code, name string
		rank       int
	}{
		{"NA", "North America", 1}, {"EU", "Europe", 2}, {"JP", "Japan", 3}, {"OCE", "Oceania", 4},
		{"Unknown", "Unknown", 0}, {"", "", 0},
	}
	for _, c := range cases {
		if got := RegionName(c.code); got != c.name {
			t.Errorf("RegionName(%q) = %q, want %q", c.code, got, c.name)
		}
		if got := RegionRank(c.code); got != c.rank {
			t.Errorf("RegionRank(%q) = %d, want %d", c.code, got, c.rank)
		}
	}
}

func TestCanonicalNames(t *testing.T) {
	cases := []struct {
		name, input, want string
		fn                func(string) string
	}{
		{"region lower", "na", "NA", CanonicalRegion},
		{"region mixed", "Oce", "OCE", CanonicalRegion},
		{"region canonical", "EU", "EU", CanonicalRegion},
		{"region unknown passes through", "xx", "xx", CanonicalRegion},
		{"region empty", "", "", CanonicalRegion},
		{"datacenter lower", "chaos", "Chaos", CanonicalDatacenter},
		{"datacenter upper", "MATERIA", "Materia", CanonicalDatacenter},
		{"datacenter unknown passes through", "nowhere", "nowhere", CanonicalDatacenter},
		{"world lower", "adamantoise", "Adamantoise", CanonicalWorld},
		{"world unknown passes through", "nowhereland", "nowhereland", CanonicalWorld},
	}
	for _, c := range cases {
		if got := c.fn(c.input); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
