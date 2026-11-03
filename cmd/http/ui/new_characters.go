package ui

import (
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// NewCharactersCard feeds the shared "new-characters-card" component
// (components/new_characters.html): the characters created in the window
// with their trend, and the company chocobos earned beside them. Before the
// snapshot has created figures it counts the company chocobos alone.
type NewCharactersCard struct {
	Window Window
	Count  int64
	Trend  Trend
	// Created marks a count of created characters; Chocobos is then the
	// company chocobos earned in the same window.
	Created  bool
	Chocobos int64
	// EstimatedBefore is the display day ("Oct 21") before which the
	// window's created days are estimated; empty when none are.
	EstimatedBefore string
}

// newCharactersCard builds the card of a scope's window totals.
func newCharactersCard(snapshot *contract.UIStatsSnapshot, totals census.NewCharacterTotals) NewCharactersCard {
	card := NewCharactersCard{Window: Window{Days: totals.Days}, Count: totals.Current, Trend: newTrend(totals)}
	if !census.CreatedReady(snapshot) {
		return card
	}
	created := totals.CreatedWindow()
	card.Count, card.Trend, card.Created, card.Chocobos = created.Current, newTrend(created), true, totals.Current
	start, err := time.Parse("2006-01-02", totals.StartDay)
	if exact, ok := census.CreatedEstimatedBefore(snapshot); ok && err == nil && exact.After(start) {
		card.EstimatedBefore = exact.Format("Jan 2")
	}
	return card
}

// newCharactersCount is a scope's new characters in the window, as the
// world tables show them: created characters, or before the snapshot has
// them the company chocobos.
func newCharactersCount(snapshot *contract.UIStatsSnapshot, totals census.NewCharacterTotals) int64 {
	if census.CreatedReady(snapshot) {
		return totals.Created
	}
	return totals.Current
}
