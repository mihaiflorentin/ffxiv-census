package ui

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// seedRegions upserts the given number of characters per region, on one
// world of that region, so the fixture snapshot has known region groups.
func seedRegions(t *testing.T, rig *testRig, counts map[string]int) {
	t.Helper()
	worlds := map[string][2]string{
		"NA":  {"Balmung", "Crystal"},
		"EU":  {"Ragnarok", "Chaos"},
		"JP":  {"Tonberry", "Elemental"},
		"OCE": {"Bismarck", "Materia"},
	}
	recent := time.Now().UTC().Add(-time.Hour)
	id := uint32(20000)
	for region, n := range counts {
		for i := 0; i < n; i++ {
			id++
			w := worlds[region]
			if err := rig.chars.Upsert(context.Background(), contract.CharacterRecord{
				ID: id, Name: "Seed", World: w[0], Datacenter: w[1], Region: region,
				Race: "Hyur", FirstSeenAt: recent, LatestAchievementAt: &recent,
			}, nil); err != nil {
				t.Fatalf("seed character: %v", err)
			}
		}
	}
}

// tableBodyRows returns the text of each data row (rows with td cells),
// header cell first, in document order.
func tableBodyRows(table *html.Node) [][]string {
	var rows [][]string
	for _, tr := range findAll(table, byAtom(atom.Tr)) {
		cells := findAll(tr, byAtom(atom.Td))
		if len(cells) == 0 {
			continue
		}
		row := []string{}
		for _, th := range findAll(tr, byAtom(atom.Th)) {
			row = append(row, textOf(th))
		}
		for _, td := range cells {
			row = append(row, textOf(td))
		}
		rows = append(rows, row)
	}
	return rows
}

// realmTally returns the section labelled by the page's h1: the dashboard's
// realm tally.
func realmTally(t *testing.T, doc *html.Node) *html.Node {
	t.Helper()
	h1s := findAll(doc, byAtom(atom.H1))
	if len(h1s) != 1 {
		t.Fatalf("expected one h1, got %d", len(h1s))
	}
	id, _ := attr(h1s[0], "id")
	sections := findAll(doc, func(n *html.Node) bool {
		v, _ := attr(n, "aria-labelledby")
		return n.DataAtom == atom.Section && id != "" && v == id
	})
	if len(sections) != 1 {
		t.Fatalf("expected one section labelled by the h1 (id %q), got %d", id, len(sections))
	}
	return sections[0]
}

func TestDashboardRealmTally(t *testing.T) {
	rig := newTestRig(t)
	seedRegions(t, rig, map[string]int{"NA": 2, "EU": 1, "JP": 1})
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	rec := serveMuxGet(t, mux, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	tally := realmTally(t, parseHTML(t, rec.Body.String()))

	t.Run("total population figure", func(t *testing.T) {
		figures := findAll(tally, func(n *html.Node) bool {
			v, _ := attr(n, "value")
			return n.DataAtom == atom.Data && v == "4"
		})
		if len(figures) != 1 || textOf(figures[0]) != "4" {
			t.Fatalf("expected one <data value=\"4\">4</data> population figure, got %d", len(figures))
		}
	})

	want := []struct {
		name, href, count, share string
	}{
		{"North America", "/ui/worlds?region=NA", "2", "50.0%"},
		{"Europe", "/ui/worlds?region=EU", "1", "25.0%"},
		{"Japan", "/ui/worlds?region=JP", "1", "25.0%"},
	}

	t.Run("one linked segment per region", func(t *testing.T) {
		var segments []*html.Node
		for _, a := range findAll(tally, byAtom(atom.A)) {
			if href, _ := attr(a, "href"); strings.HasPrefix(href, "/ui/worlds?region=") {
				segments = append(segments, a)
			}
		}
		if len(segments) != len(want) {
			t.Fatalf("expected %d region segments, got %d", len(want), len(segments))
		}
		for i, w := range want {
			seg := segments[i]
			if href, _ := attr(seg, "href"); href != w.href {
				t.Errorf("segment %d href = %q, want %q", i, href, w.href)
			}
			if ti, ok := attr(seg, "tabindex"); ok && ti == "-1" {
				t.Errorf("segment %d is removed from the tab order", i)
			}
			label, _ := attr(seg, "aria-label")
			for _, part := range []string{w.name, w.share} {
				if !strings.Contains(label, part) {
					t.Errorf("segment %d accessible name %q missing %q", i, label, part)
				}
			}
			proportional := findAll(seg, func(n *html.Node) bool {
				v, _ := attr(n, "width")
				return v == strings.TrimSuffix(w.share, "%")+"%"
			})
			if len(proportional) == 0 {
				t.Errorf("segment %d has no mark sized to its share %s", i, w.share)
			}
		}
	})

	t.Run("equivalent text table", func(t *testing.T) {
		var table *html.Node
		for _, tb := range findAll(tally, byAtom(atom.Table)) {
			for _, c := range findAll(tb, byAtom(atom.Caption)) {
				if strings.Contains(textOf(c), "Population by region") {
					table = tb
				}
			}
		}
		if table == nil {
			t.Fatal("expected a table captioned 'Population by region' in the realm tally")
		}
		body := tableBodyRows(table)
		if len(body) != len(want) {
			t.Fatalf("expected %d table rows, got %d: %v", len(want), len(body), body)
		}
		for i, w := range want {
			got := strings.Join(body[i], "|")
			if got != w.name+"|"+w.count+"|"+w.share {
				t.Errorf("row %d = %q, want %q", i, got, w.name+"|"+w.count+"|"+w.share)
			}
		}
	})
}
