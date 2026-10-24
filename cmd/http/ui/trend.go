package ui

import (
	"fmt"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// TrendDirection is the direction of a Trend; empty when there is nothing to
// compare.
type TrendDirection string

const (
	TrendUp   TrendDirection = "up"
	TrendDown TrendDirection = "down"
	TrendFlat TrendDirection = "flat"
)

// Trend describes the change between two window totals so a template can
// show it with a direction word, a signed delta and an arrow icon, never by
// colour alone.
type Trend struct {
	// Direction is "up", "down" or "flat"; empty when both windows are zero.
	Direction TrendDirection
	// Text reads like "Up +20 (+200.0%)", "Down −15 (−75.0%)" or "No change".
	Text string
	// Window is the length of both windows, for "on the previous 90 days".
	Window Window
}

// minus is the typographic minus sign used for negative deltas.
const minus = "−"

// newTrend compares a new-characters window total with the previous
// window's. A zero previous window omits the percentage.
func newTrend(totals census.NewCharacterTotals) Trend {
	current, previous := totals.Current, totals.Previous
	window := Window{Days: totals.Days}
	delta := current - previous
	switch {
	case delta == 0 && previous == 0:
		return Trend{}
	case delta == 0:
		return Trend{Direction: TrendFlat, Text: "No change", Window: window}
	}
	word, sign, direction := "Up", "+", TrendUp
	absolute := delta
	if delta < 0 {
		word, sign, direction = "Down", minus, TrendDown
		absolute = -delta
	}
	text := fmt.Sprintf("%s %s%s", word, sign, formatNumber(absolute))
	if previous > 0 {
		text += fmt.Sprintf(" (%s%s)", sign, formatPercent(absolute, previous))
	}
	return Trend{Direction: direction, Text: text, Window: window}
}
