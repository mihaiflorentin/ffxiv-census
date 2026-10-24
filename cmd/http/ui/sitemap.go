package ui

import (
	"encoding/xml"
	"io"
	"net/http"
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
var sitemapStaticPaths = []string{"/", "/ui/races", "/ui/grand-companies", "/ui/jobs", "/ui/worlds", "/ui/expansions", "/ui/methodology"}

// Sitemap handles GET /sitemap.xml. It renders the sitemap-protocol document
// for every indexable page from the statistics snapshot. A sitemap must never
// fail: when no usable snapshot exists it degrades to the static URLs only,
// still answering 200.
func (c *UIController) Sitemap(w http.ResponseWriter, r *http.Request) {
	baseURL := c.baseURL

	var lastmod string
	var worldPaths []string
	if c.stats != nil {
		snapshot, _, err := c.stats.Current(r.Context())
		if err == nil && snapshot != nil {
			lastmod = snapshot.GeneratedAt.UTC().Format(time.RFC3339)
			for _, row := range census.SnapshotGroups(snapshot, "world", contract.StatsScope{}) {
				if !isIndexableWorld(row.Key) {
					continue
				}
				worldPaths = append(worldPaths, "/ui/worlds/"+row.Key)
			}
		}
	}

	set := sitemapURLSet{XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, path := range sitemapStaticPaths {
		set.URLs = append(set.URLs, sitemapURL{Loc: baseURL + path, LastMod: lastmod})
	}
	for _, path := range worldPaths {
		set.URLs = append(set.URLs, sitemapURL{Loc: baseURL + path, LastMod: lastmod})
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(set)
}
