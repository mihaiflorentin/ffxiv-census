package ui

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"
)

// The freshness line and the next-refresh state themselves are covered by
// TestNightlyFreshnessLine and TestNextRefreshState; this covers what
// nightly does to the time it is given.
func TestNightlyFreshnessReadsTheRebuildDayInUTC(t *testing.T) {
	tmpl := template.Must(template.New("components").Funcs(templateFuncs).ParseFS(content, componentsTemplate))
	// 01:30 on the 25th in UTC+3 is still the 24th in UTC.
	at := time.Date(2026, 10, 25, 1, 30, 0, 0, time.FixedZone("EEST", 3*60*60))
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "freshness", nightly(at)); err != nil {
		t.Fatal(err)
	}
	want := `Rebuilt nightly, last on <time datetime="2026-10-24T22:30:00Z">24 October 2026</time>`
	if !strings.Contains(buf.String(), want) {
		t.Errorf("freshness = %s\nwant it to contain %s", buf.String(), want)
	}
}
