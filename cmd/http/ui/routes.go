package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/container"
)

//go:embed templates/* templates/partials/* assets/*
var content embed.FS

// Register mounts the UI routes and static assets on mux and returns the
// server handler: mux wrapped in the styled not-found fallback.
func Register(mux *http.ServeMux) http.Handler {
	svc := container.Load.CensusService()
	stats := container.Load.UIStatsService()
	q := container.Load.Queue()
	ctrl := NewUIController(svc, q, stats, container.Load.Config().App.BaseURL)
	ctrl.RegisterRoutes(mux)
	return ctrl.NotFoundFallback(mux)
}

// RegisterRoutes mounts all controller routes and the embedded asset server to the mux.
func (c *UIController) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", c.Dashboard)
	mux.HandleFunc("GET /ui/dashboard", c.Dashboard)
	mux.HandleFunc("GET /ui/partials/world-breakdown", c.WorldDrilldown)
	mux.HandleFunc("GET /ui/races", c.Races)
	mux.HandleFunc("GET /ui/grand-companies", c.GrandCompanies)
	mux.HandleFunc("GET /ui/jobs", c.Jobs)
	mux.HandleFunc("GET /ui/worlds/{world}", c.WorldDetail)
	mux.HandleFunc("GET /ui/worlds", c.Worlds)
	mux.HandleFunc("GET /ui/expansions", c.Expansions)
	mux.HandleFunc("GET /ui/methodology", c.Methodology)
	mux.HandleFunc("GET /sitemap.xml", c.Sitemap)
	mux.HandleFunc("GET /favicon.ico", c.Favicon)
	mux.Handle("GET /ui/assets/", http.StripPrefix("/ui/assets/", assetHandler()))
	// Any other path under /ui/ (including the disabled routes below) gets the
	// styled not-found page.
	mux.HandleFunc("GET /ui/", c.NotFound)
	// Personal info routes are currently disabled to protect player privacy.
	// mux.HandleFunc("GET /ui/characters/{id}", c.CharacterDetail)
	// mux.HandleFunc("GET /ui/characters", c.CharacterList)
	// mux.HandleFunc("GET /ui/characters/search", c.CharacterSearch)
}

// fontCacheControl lets browsers keep vendored fonts for a year: a font file
// that changes is shipped under a new name.
const fontCacheControl = "public, max-age=31536000, immutable"

// assetHandler serves the embedded assets directory. WOFF2 fonts get an
// explicit content type (Go's built-in MIME table has no .woff2 entry, so it
// would otherwise depend on the host's mime.types) and long-lived caching.
func assetHandler() http.Handler {
	assets, err := fs.Sub(content, "assets")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(path.Ext(r.URL.Path), ".woff2") {
			w.Header().Set("Content-Type", "font/woff2")
			w.Header().Set("Cache-Control", fontCacheControl)
		}
		fileServer.ServeHTTP(w, r)
	})
}

// NotFoundFallback wraps the server's mux so a GET or HEAD for a path no route
// matches gets the styled 404 page instead of the mux's plain-text one. Paths
// under /api/ keep the plain 404 their clients expect, and any request a route
// matches (including a handler's own 404, a method mismatch's 405 or a
// trailing-slash redirect) is served by the mux unchanged.
func (c *UIController) NotFoundFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") {
			if _, pattern := mux.Handler(r); pattern == "" {
				c.NotFound(w, r)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
