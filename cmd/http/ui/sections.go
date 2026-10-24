package ui

import "time"

// Freshness feeds the shared "freshness" component: how often a snapshot
// section is rebuilt and when it last was. A zero At renders nothing.
type Freshness struct {
	Cadence string
	At      time.Time
}

// nightly builds the freshness line for a section the full nightly refresh
// rebuilds (and the incremental refresh carries forward):
//
//	{{template "freshness" (nightly .Data.JobsBuiltAt)}}
func nightly(at time.Time) Freshness {
	return Freshness{Cadence: "nightly", At: at.UTC()}
}

// NextRefresh feeds the shared "next-refresh" state: a page whose snapshot
// section is missing because the stored snapshot predates it. Subject is the
// plural noun phrase for what is missing ("Grand Company figures").
type NextRefresh struct {
	Subject string
}

// nextRefresh builds the "arrives with the next refresh" state:
//
//	{{template "next-refresh" (nextRefresh "Grand Company figures")}}
func nextRefresh(subject string) NextRefresh {
	return NextRefresh{Subject: subject}
}
