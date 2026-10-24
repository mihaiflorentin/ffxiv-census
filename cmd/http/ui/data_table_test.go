package ui

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// tableCaptioned returns the table whose caption is exactly caption.
func tableCaptioned(t *testing.T, doc *html.Node, caption string) *html.Node {
	t.Helper()
	for _, tb := range findAll(doc, byAtom(atom.Table)) {
		for _, c := range findAll(tb, byAtom(atom.Caption)) {
			if textOf(c) == caption {
				return tb
			}
		}
	}
	t.Fatalf("no table captioned %q", caption)
	return nil
}

// rowCells returns the direct th/td cells of a row.
func rowCells(tr *html.Node) []*html.Node {
	var cells []*html.Node
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.DataAtom == atom.Th || c.DataAtom == atom.Td {
			cells = append(cells, c)
		}
	}
	return cells
}

// ownRows returns the rows of table's own thead/tbody, not of nested tables.
func ownRows(table *html.Node, section atom.Atom) []*html.Node {
	var rows []*html.Node
	for s := table.FirstChild; s != nil; s = s.NextSibling {
		if s.DataAtom != section {
			continue
		}
		for tr := s.FirstChild; tr != nil; tr = tr.NextSibling {
			if tr.DataAtom == atom.Tr {
				rows = append(rows, tr)
			}
		}
	}
	return rows
}

// assertDataTable checks the shared data-table contract: the table scrolls
// inside a focusable, named region; every body row starts with a row header;
// and the named numeric columns are right-aligned in header and body.
func assertDataTable(t *testing.T, doc, table *html.Node, numeric ...string) {
	t.Helper()
	wrap := table.Parent
	if role, _ := attr(wrap, "role"); role != "region" {
		t.Errorf("table must sit in a scroll region (role=region), parent <%s role=%q>", wrap.Data, role)
	}
	if tab, _ := attr(wrap, "tabindex"); tab != "0" {
		t.Errorf("scroll region must be focusable for keyboard scrolling, tabindex=%q", tab)
	}
	name, _ := attr(wrap, "aria-label")
	if ref, ok := attr(wrap, "aria-labelledby"); ok {
		if n := byID(doc, ref); n != nil {
			name = textOf(n)
		}
	}
	if strings.TrimSpace(name) == "" {
		t.Error("scroll region needs an accessible name")
	}

	heads := ownRows(table, atom.Thead)
	if len(heads) != 1 {
		t.Fatalf("expected one header row, got %d", len(heads))
	}
	headers := rowCells(heads[0])
	columns := map[string]int{}
	for i, th := range headers {
		columns[textOf(th)] = i
	}
	body := ownRows(table, atom.Tbody)
	if len(body) == 0 {
		t.Fatal("table has no body rows")
	}
	for _, col := range numeric {
		i, ok := columns[col]
		if !ok {
			t.Errorf("no column %q in %v", col, columns)
			continue
		}
		if !hasClass(headers[i], "text-right") {
			t.Errorf("numeric column header %q is not right-aligned", col)
		}
		for _, tr := range body {
			cells := rowCells(tr)
			if len(cells) == len(headers) && !hasClass(cells[i], "text-right") {
				t.Errorf("numeric cell %q in column %q is not right-aligned", textOf(cells[i]), col)
			}
		}
	}
	for _, tr := range body {
		cells := rowCells(tr)
		if len(cells) != len(headers) {
			continue // a full-width detail row, e.g. a drill-down
		}
		if cells[0].DataAtom != atom.Th {
			t.Errorf("row %q must start with a row header", textOf(tr))
		}
	}
}

// rowHeaderLinks maps each body row header's link text to its href.
func rowHeaderLinks(table *html.Node) map[string]string {
	links := map[string]string{}
	for _, tr := range ownRows(table, atom.Tbody) {
		cells := rowCells(tr)
		if len(cells) == 0 || cells[0].DataAtom != atom.Th {
			continue
		}
		for _, a := range findAll(cells[0], byAtom(atom.A)) {
			href, _ := attr(a, "href")
			links[textOf(a)] = href
		}
	}
	return links
}

func TestWorldRankingsUseTheDataTable(t *testing.T) {
	doc := renderPage(t, "/ui/worlds?region=EU&dc=Chaos")
	table := tableCaptioned(t, doc, "World rankings, on the Chaos datacenter")
	assertDataTable(t, doc, table, "Characters", "Active, 30 days", "New, 30 days", "Active share")
	if got := rowHeaderLinks(table)["Cerberus"]; got != "/ui/worlds/Cerberus" {
		t.Errorf("world name links to %q, want /ui/worlds/Cerberus", got)
	}
}

func TestRaceTableUsesTheDataTable(t *testing.T) {
	doc := renderPage(t, "/ui/races")
	table := tableCaptioned(t, doc, "Characters by race, across every region")
	assertDataTable(t, doc, table, "Characters", "Share", "Active, 30 days", "Active share")
}

func TestDashboardRegionTableKeepsDrilldownTriggerInFirstColumn(t *testing.T) {
	doc := renderPage(t, "/")
	table := tableCaptioned(t, doc, "Characters by region")
	assertDataTable(t, doc, table, "Characters", "Active, 30 days", "Active share")
	triggers := 0
	for _, tr := range ownRows(table, atom.Tbody) {
		cells := rowCells(tr)
		if len(cells) < 2 {
			continue
		}
		// The first column stays in view when the table scrolls sideways on
		// a phone, so the drill-down trigger lives there.
		for _, b := range findAll(cells[0], byAtom(atom.Button)) {
			if ref, _ := attr(b, "aria-controls"); ref != "" && byID(doc, ref) != nil {
				triggers++
			}
		}
	}
	if triggers == 0 {
		t.Fatal("expected a drill-down trigger in each region's row header")
	}
}

func TestWorldDrilldownUsesTheDataTable(t *testing.T) {
	rig := newTestRig(t)
	seedFullDashboard(t, rig)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)
	rec := serveMuxGet(t, mux, "/ui/partials/world-breakdown?region=NA")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	doc := parseHTML(t, rec.Body.String())
	table := tableCaptioned(t, doc, "Worlds in North America")
	assertDataTable(t, doc, table, "Characters", "Active, 30 days", "New, 30 days", "Active share")
	if got := rowHeaderLinks(table)["Balmung"]; got != "/ui/worlds/Balmung" {
		t.Errorf("drill-down world links to %q, want /ui/worlds/Balmung", got)
	}
	if styled := findAll(doc, func(n *html.Node) bool { _, ok := attr(n, "style"); return ok }); len(styled) > 0 {
		t.Errorf("drill-down carries %d inline style attributes", len(styled))
	}
}
