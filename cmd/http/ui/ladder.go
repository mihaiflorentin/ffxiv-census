package ui

import "fmt"

// ladderTopSize is how many rungs a ladder shows before "Show all".
const ladderTopSize = 10

// Ladder feeds the shared "ladder" component (components/ladder.html): a
// ranked board with oversized rank numerals in a left gutter, the leader set
// apart by type size, and a hairline bar per rung measured against the
// leader. The top ten show; the rest wait behind a native "Show all"
// disclosure, so expanding needs no request. Used by the world and
// datacenter Rankings and the Free Company boards.
type Ladder struct {
	// Caption names the board for the "Show the numbers" table.
	Caption string
	// NameHeading, NoteHeading and ValueHeading head the table columns
	// ("World", "Datacenter", "Per 1,000 residents"); DetailHeading heads
	// the optional detail column.
	NameHeading, NoteHeading, ValueHeading, DetailHeading string
	// Top are the first rungs, Rest the ones behind "Show all".
	Top, Rest []LadderRung
	// AllLabel is the disclosure's summary ("Show all 85 worlds").
	AllLabel string
	// Entrance marks the ladder as the page's one entrance.
	Entrance bool
	// Tone colours the bars with a semantic mark colour (a Grand Company
	// slug: "maelstrom"); empty keeps Aether.
	Tone string
}

// LadderRung is one ranked entry.
type LadderRung struct {
	Rank int
	// Name is the entry; Href links it (empty for no link).
	Name, Href string
	// Note sits under the name (a world's datacenter, a Free Company's
	// world); NoteHref links it.
	Note, NoteHref string
	// Value is the ranked figure, formatted, and Unit what it counts
	// ("per 1,000", "characters"); Detail is a quieter supporting line
	// ("9 new of 300 characters").
	Value, Unit, Detail string
	// Bar is the rung's value against the leader's, as an SVG width ("62.500%").
	Bar    string
	Leader bool
	// ExternalHref is an outbound link for the entry (a Free Company's
	// Lodestone page), labelled ExternalLabel ("Lodestone").
	ExternalHref, ExternalLabel string
}

// newLadder splits rungs into the top ten and the rest.
func newLadder(rungs []LadderRung, noun string) Ladder {
	l := Ladder{Top: rungs}
	if len(rungs) > ladderTopSize {
		l.Top, l.Rest = rungs[:ladderTopSize], rungs[ladderTopSize:]
		l.AllLabel = fmt.Sprintf("Show all %d %s", len(rungs), noun)
	}
	return l
}

// barWidth is share (0 to 1) as an SVG percentage width.
func barWidth(share float64) string {
	return fmt.Sprintf("%.3f%%", share*100)
}
