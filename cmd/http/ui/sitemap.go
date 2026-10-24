package ui

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"time"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// sitemapURLSet is the sitemap-protocol <urlset> document root.
type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// sitemapURL is one sitemap-protocol <url> entry.
type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// sitemapStaticPaths lists the indexable UI pages without per-entity routes.
var sitemapStaticPaths = []string{"/", "/ui/races", "/ui/grand-companies", "/ui/jobs", "/ui/worlds", "/ui/expansions", "/ui/methodology", "/ui/privacy", "/ui/rankings", "/ui/rankings/free-companies"}

// Sitemap handles GET /sitemap.xml. It renders the sitemap-protocol document
// for every indexable page from the statistics snapshot. A sitemap must never
// fail: when no usable snapshot exists it degrades to the static URLs only,
// still answering 200.
func (c *UIController) Sitemap(w http.ResponseWriter, r *http.Request) {
	baseURL := c.baseURL

	lastmod := sitemapLastMods(nil)
	var worldPaths []string
	if c.stats != nil {
		snapshot, _, err := c.stats.Current(r.Context())
		if err == nil && snapshot != nil {
			lastmod = sitemapLastMods(snapshot)
			for _, row := range census.SnapshotGroups(snapshot, "world", contract.StatsScope{}) {
				if !isIndexableWorld(row.Key) {
					continue
				}
				worldPaths = append(worldPaths, sitemapWorldPath(row.Key))
			}
		}
	}

	set := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, path := range append(append([]string{}, sitemapStaticPaths...), worldPaths...) {
		set.URLs = append(set.URLs, sitemapURL{Loc: baseURL + path, LastMod: formatLastMod(lastmod(path))})
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(set)
}

// sitemapLastMods returns, per sitemap path, when that page's figures last
// changed, so Search Console can trust lastmod. Pages fed by every refresh
// use the snapshot time; Jobs, Free Company rankings and Private
// Achievements use their nightly section's build time; Methodology shows no
// snapshot data and gets none. A nil snapshot gives no lastmod at all.
func sitemapLastMods(snapshot *contract.UIStatsSnapshot) func(path string) time.Time {
	return func(path string) time.Time {
		if snapshot == nil {
			return time.Time{}
		}
		switch path {
		case "/ui/methodology":
			return time.Time{}
		case "/ui/jobs":
			if snapshot.Jobs == nil {
				return time.Time{}
			}
			return snapshot.Jobs.BuiltAt
		case "/ui/privacy":
			return snapshot.PrivacyBuiltAt
		case "/ui/rankings/free-companies":
			return snapshot.FreeCompaniesBuiltAt
		default:
			return snapshot.GeneratedAt
		}
	}
}

// formatLastMod renders a sitemap lastmod, empty for an unknown time so the
// element is omitted.
func formatLastMod(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// sitemapWorldPath is a world page's path with the name escaped, so every
// <loc> is a valid URL.
func sitemapWorldPath(world string) string {
	return "/ui/worlds/" + url.PathEscape(world)
}
