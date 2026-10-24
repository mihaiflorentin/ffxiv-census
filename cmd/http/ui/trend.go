package ui

import "fmt"

// Trend describes the change between two window totals so a template can
// show it with a direction word, a signed delta and an arrow icon, never by
// colour alone.
type Trend struct {
	// Direction is "up", "down" or "flat"; empty when both windows are zero.
	Direction string
	// Text reads like "Up +20 (+200.0%)", "Down −15 (−75.0%)" or "No change".
	Text string
}

// minus is the typographic minus sign used for negative deltas.
const minus = "−"

// newTrend compares a current window total with the previous one. A zero
// previous window omits the percentage.
func newTrend(current, previous int64) Trend {
	delta := current - previous
	switch {
	case delta == 0 && previous == 0:
		return Trend{}
	case delta == 0:
		return Trend{Direction: "flat", Text: "No change"}
	}
	word, sign, direction := "Up", "+", "up"
	absolute := delta
	if delta < 0 {
		word, sign, direction = "Down", minus, "down"
		absolute = -delta
	}
	text := fmt.Sprintf("%s %s%s", word, sign, formatNumber(absolute))
	if previous > 0 {
		text += fmt.Sprintf(" (%s%s)", sign, formatPercent(absolute, previous))
	}
	return Trend{Direction: direction, Text: text}
}
