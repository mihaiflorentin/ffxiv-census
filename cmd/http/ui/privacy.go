package ui

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// privacyBoardSize is how many worlds each of the most and least private
// lists shows.
const privacyBoardSize = 5

// VeilSegment is one stretch of the veil band: X and Width are percentages
// of the band ("12.500%"), so the private hatching is never stretched.
type VeilSegment struct {
	// Kind is public, private or unchecked (the segment's class suffix).
	Kind, Label string
	Count       int64
	Share       string
	X, Width    string
}

// PrivacyRow is one region or world in the privacy tables and lists.
type PrivacyRow struct {
	Key, Name, Href, Datacenter               string
	Total, Public, Private, Unchecked         int64
	PublicShare, PrivateShare, UncheckedShare string
	PrivateShareValue                         float64
	// BarWidth is the world's private share against the most private
	// world's ("62.500%"), for the hatched bar of the world lists.
	BarWidth string
}

// PrivacyWorldList feeds the shared privacy-worlds block of the page.
type PrivacyWorldList struct {
	ID, Title, Meta string
	Rows            []PrivacyRow
}

func privacyWorldsFor(id, title, meta string, rows []PrivacyRow) PrivacyWorldList {
	return PrivacyWorldList{ID: id, Title: title, Meta: meta, Rows: rows}
}

// PrivacyViewData feeds /ui/privacy.
type PrivacyViewData struct {
	// Ready is false while the stored snapshot has no privacy figures; the
	// page then shows the next-refresh state.
	Ready   bool
	BuiltAt time.Time
	Total   int64
	Overall PrivacyRow
	Veil    []VeilSegment
	Regions []PrivacyRow
	// MostPrivate and LeastPrivate are the world lists; Excluded worlds had
	// fewer than MinimumSize characters.
	MostPrivate, LeastPrivate []PrivacyRow
	MinimumSize               int64
	Excluded                  int
}

// PrivacyNote is the one-sentence privacy note on the dashboard and the
// Expansions page. Share is the census-wide private share.
type PrivacyNote struct {
	Ready bool
	Share string
}

// Privacy handles GET /ui/privacy.
func (c *UIController) Privacy(w http.ResponseWriter, r *http.Request) {
	snapshot, state, ok := c.currentStats(w, r)
	if !ok {
		return
	}
	data := PrivacyViewData{Ready: census.PrivacyReady(snapshot)}
	if data.Ready {
		data.BuiltAt = snapshot.PrivacyBuiltAt
		overall := c.memo.Privacy(snapshot, contract.StatsScope{})
		data.Total = overall.Total
		data.Overall = newPrivacyRow(overall)
		data.Veil = veilSegments(overall)
		for _, region := range c.memo.PrivacyRegions(snapshot) {
			row := newPrivacyRow(region)
			row.Name = regionDisplayName(region.Key)
			data.Regions = append(data.Regions, row)
		}
		board := c.memo.PrivacyWorlds(snapshot, "", privacyBoardSize)
		data.MinimumSize, data.Excluded = board.MinimumSize, board.Excluded
		var top float64
		if len(board.MostPrivate) > 0 {
			top = board.MostPrivate[0].PrivateShare
		}
		data.MostPrivate = worldPrivacyRows(board.MostPrivate, top)
		data.LeastPrivate = worldPrivacyRows(board.LeastPrivate, top)
	}
	c.render(w, "templates/privacy.html", statsPageData("Private Achievements", "methodology", "/ui/privacy",
		"How many Final Fantasy XIV characters hide their achievements, how many the census has not checked yet, where privacy is most common, and how it lowers the activity and story figures.",
		state, data))
}

// PrivacyNoteLine is a PrivacyNote with the page's consequence clause, for
// the shared privacy-note component:
//
//	{{template "privacy-note" (privacyNoteFor .Data.PrivacyNote "they can’t count as active")}}
type PrivacyNoteLine struct {
	PrivacyNote
	Consequence string
}

func privacyNoteFor(note PrivacyNote, consequence string) PrivacyNoteLine {
	return PrivacyNoteLine{PrivacyNote: note, Consequence: consequence}
}

// privacyNote builds the dashboard and Expansions privacy note.
func (c *UIController) privacyNote(snapshot *contract.UIStatsSnapshot) PrivacyNote {
	if !census.PrivacyReady(snapshot) {
		return PrivacyNote{}
	}
	return PrivacyNote{Ready: true, Share: percentOf(c.memo.Privacy(snapshot, contract.StatsScope{}).PrivateShare)}
}

func newPrivacyRow(p census.PrivacyShare) PrivacyRow {
	return PrivacyRow{
		Key: p.Key, Name: p.Key,
		Total: p.Total, Public: p.Public(), Private: p.Private, Unchecked: p.Unchecked,
		PublicShare:       percentOf(p.PublicShare),
		PrivateShare:      percentOf(p.PrivateShare),
		UncheckedShare:    percentOf(p.UncheckedShare),
		PrivateShareValue: p.PrivateShare * 100,
	}
}

func worldPrivacyRows(shares []census.PrivacyShare, top float64) []PrivacyRow {
	rows := make([]PrivacyRow, 0, len(shares))
	for _, s := range shares {
		row := newPrivacyRow(s)
		row.BarWidth = "0%"
		if top > 0 {
			row.BarWidth = fmt.Sprintf("%.3f%%", s.PrivateShare/top*100)
		}
		row.Href = "/ui/worlds/" + url.PathEscape(s.Key)
		row.Datacenter = census.WorldDatacenter(s.Key)
		rows = append(rows, row)
	}
	return rows
}

// veilSegments splits the band into public, private and unchecked, in that
// order, leaving out empty segments.
func veilSegments(p census.PrivacyShare) []VeilSegment {
	parts := []struct {
		kind, label string
		count       int64
		share       float64
	}{
		{"public", "Public", p.Public(), p.PublicShare},
		{"private", "Private", p.Private, p.PrivateShare},
		{"unchecked", "Not yet checked", p.Unchecked, p.UncheckedShare},
	}
	var out []VeilSegment
	var x float64
	for _, part := range parts {
		if part.count <= 0 {
			continue
		}
		out = append(out, VeilSegment{
			Kind: part.kind, Label: part.label, Count: part.count, Share: percentOf(part.share),
			X: fmt.Sprintf("%.3f%%", x*100), Width: fmt.Sprintf("%.3f%%", part.share*100),
		})
		x += part.share
	}
	return out
}
