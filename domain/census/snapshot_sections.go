package census

import "github.com/mihaiflorentin88/ffxiv-census/port/contract"

// SnapshotSection names a part of the UI statistics snapshot that was added
// after the oldest schema readers still accept (contract.UIStatsMinSchemaVersion).
// A page that needs one asks SnapshotHasSection and shows the
// "arrives with the next refresh" state when the stored snapshot predates it.
//
// Adding a section: declare its constant and the schema version that
// introduced it in sectionSince.
type SnapshotSection string

const (
	// SectionGrandCompanies is the grand_company and grand_company_race groups.
	SectionGrandCompanies SnapshotSection = "grand_companies"
	// SectionJobs is the per-world job section (snapshot.Jobs).
	SectionJobs SnapshotSection = "jobs"
	// SectionPrivacy is the at cap, private and unchecked location counts and
	// UIStatsSnapshot.PrivacyBuiltAt (see PrivacyReady).
	SectionPrivacy SnapshotSection = "privacy"
	// SectionFreeCompanies is the in a Free Company location counts and the
	// Free Company boards' own row, named by
	// UIStatsSnapshot.FreeCompaniesBuiltAt.
	SectionFreeCompanies SnapshotSection = "free_companies"
)

// sectionSince is the first snapshot schema version carrying each section.
var sectionSince = map[SnapshotSection]int{
	SectionGrandCompanies: 3,
	SectionJobs:           3,
	SectionPrivacy:        3,
	SectionFreeCompanies:  3,
}

// sectionPresent is an extra presence check for a section stored as its own
// payload field: a snapshot of the right version written before the field
// existed lacks it until the next full rebuild.
var sectionPresent = map[SnapshotSection]func(*contract.UIStatsSnapshot) bool{
	SectionJobs:          func(s *contract.UIStatsSnapshot) bool { return s.Jobs != nil },
	SectionFreeCompanies: func(s *contract.UIStatsSnapshot) bool { return !s.FreeCompaniesBuiltAt.IsZero() },
}

// SnapshotHasSection reports whether snapshot carries section: its schema is
// at least the version that introduced it. Unknown sections are absent.
func SnapshotHasSection(snapshot *contract.UIStatsSnapshot, section SnapshotSection) bool {
	since, ok := sectionSince[section]
	if !ok || snapshot == nil || snapshot.SchemaVersion < since {
		return false
	}
	if present, ok := sectionPresent[section]; ok {
		return present(snapshot)
	}
	return true
}
