package ui

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
	"path"
	"strings"

	census "github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// UIController handles all web interface views and HTMX partials.
type UIController struct {
	svc              *census.Service
	stats            *census.UIStatsService
	q                contract.Queue
	baseURL          string
	pageTemplates    map[string]*template.Template
	partialTemplates map[string]*template.Template
	// rankings memoises the Rankings boards per snapshot.
	rankings *census.RankingsMemo
}

// NewUIController constructs a new UI controller.
func NewUIController(svc *census.Service, q contract.Queue, stats *census.UIStatsService, baseURL string) *UIController {
	controller := &UIController{
		svc:              svc,
		stats:            stats,
		q:                q,
		baseURL:          strings.TrimRight(baseURL, "/"),
		pageTemplates:    make(map[string]*template.Template),
		partialTemplates: make(map[string]*template.Template),
		rankings:         census.NewRankingsMemo(),
	}
	for _, page := range []string{
		"templates/dashboard.html",
		"templates/races.html",
		"templates/grand_companies.html",
		"templates/worlds.html",
		"templates/world_detail.html",
		"templates/expansions.html",
		"templates/methodology.html",
		"templates/privacy.html",
		"templates/rankings.html",
		"templates/character.html",
		"templates/characters_list.html",
		notFoundTemplate,
		unavailableTemplate,
	} {
		controller.pageTemplates[page] = template.Must(template.New("layout.html").Funcs(templateFuncs).ParseFS(content, "templates/layout.html", componentsTemplate, page))
	}
	for _, partial := range []string{
		"templates/partials/world_drilldown.html",
		"templates/partials/search_results.html",
	} {
		parsed := template.Must(template.New("partial").Funcs(templateFuncs).ParseFS(content, partial, componentsTemplate))
		controller.partialTemplates[partial] = parsed.Lookup(path.Base(partial))
	}
	return controller
}

// PageData represents common data passed to all top-level UI page templates.
type PageData struct {
	Title         string
	ActiveNav     string
	BaseURL       string
	CanonicalPath string
	Description   string
	SearchQuery   string
	StatsUpdated  string
	StatsStale    bool
	// NoIndex marks error pages: they carry a robots noindex hint and no
	// canonical URL.
	NoIndex bool
	Data    any
}

const (
	componentsTemplate  = "templates/components/*.html"
	notFoundTemplate    = "templates/not_found.html"
	unavailableTemplate = "templates/unavailable.html"
)

// UnavailableViewData feeds the 503 "statistics are being prepared" page.
type UnavailableViewData struct {
	RetryPath string
}

// NotFoundViewData feeds the styled 404 page.
type NotFoundViewData struct {
	Heading string
	Message string
}

func (c *UIController) currentStats(w http.ResponseWriter, r *http.Request) (*contract.UIStatsSnapshot, census.UIStatsState, bool) {
	if c.stats == nil {
		c.renderUnavailable(w, r)
		return nil, census.UIStatsState{}, false
	}
	snapshot, state, err := c.stats.Current(r.Context())
	if err != nil {
		logging.Error("ui.stats", err.Error())
		c.renderUnavailable(w, r)
		return nil, census.UIStatsState{}, false
	}

	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("Vary", "HX-Request, HX-Target, HX-History-Restore-Request, Accept-Encoding")
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s", snapshot.GeneratedAt.UnixNano(), r.URL.Path, r.URL.Query().Encode(), strings.ToLower(r.Header.Get("HX-Request")), r.Header.Get("HX-Target"), strings.ToLower(r.Header.Get("HX-History-Restore-Request")))
	etag := fmt.Sprintf(`"ui-%x"`, h.Sum64())
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil, state, false
	}
	return snapshot, state, true
}

func statsPageData(title, activeNav, canonicalPath, description string, state census.UIStatsState, data any) PageData {
	return PageData{
		Title:         title,
		ActiveNav:     activeNav,
		CanonicalPath: canonicalPath,
		Description:   description,
		StatsUpdated:  formatStatsUpdated(state),
		StatsStale:    state.Stale,
		Data:          data,
	}
}

// renderUnavailable answers 503 with Retry-After while no statistics snapshot
// can be served. Browsers get the styled "being prepared" page in the shell;
// HTMX fragment requests keep a short plain-text body, which HTMX does not swap.
func (c *UIController) renderUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "60")
	if strings.EqualFold(r.Header.Get("HX-Request"), "true") {
		http.Error(w, "Statistics are temporarily unavailable. Please retry shortly.", http.StatusServiceUnavailable)
		return
	}
	c.renderStatus(w, unavailableTemplate, PageData{
		Title:         "Statistics are being prepared",
		CanonicalPath: r.URL.Path,
		Description:   "The FFXIV Census statistics snapshot is being prepared. Try again shortly.",
		NoIndex:       true,
		Data:          UnavailableViewData{RetryPath: r.URL.RequestURI()},
	}, http.StatusServiceUnavailable)
}

// NotFound renders the styled 404 page for any unknown route under /ui/.
func (c *UIController) NotFound(w http.ResponseWriter, r *http.Request) {
	c.renderNotFound(w, r, NotFoundViewData{
		Heading: "Page not found",
		Message: "This page is not part of the census. The link may be out of date, or the address may have a typo.",
	})
}

// renderNotFound answers 404 through the page shell. The footer still shows
// snapshot freshness when a snapshot is available, but a missing snapshot
// never turns a 404 into a 503.
func (c *UIController) renderNotFound(w http.ResponseWriter, r *http.Request, view NotFoundViewData) {
	data := PageData{
		Title:         view.Heading,
		CanonicalPath: r.URL.Path,
		Description:   view.Message,
		NoIndex:       true,
		Data:          view,
	}
	c.fillFreshness(&data)
	// A not-found page shows no figures, so the stale notice would be noise.
	data.StatsStale = false
	c.renderStatus(w, notFoundTemplate, data, http.StatusNotFound)
}

// fillFreshness sets the footer freshness for pages that do not otherwise
// depend on the snapshot. It reads only the snapshot already held in memory,
// so these pages never wait on (or trigger) a database load; with nothing
// cached they render without freshness.
func (c *UIController) fillFreshness(data *PageData) {
	if c.stats == nil {
		return
	}
	if state, ok := c.stats.Cached(); ok {
		data.StatsUpdated = formatStatsUpdated(state)
		data.StatsStale = state.Stale
	}
}

// render executes a template compiled once at controller construction.
func (c *UIController) render(w http.ResponseWriter, pageTemplate string, data PageData) {
	c.renderStatus(w, pageTemplate, data, http.StatusOK)
}

// renderStatus executes a precompiled page template with the given status code.
func (c *UIController) renderStatus(w http.ResponseWriter, pageTemplate string, data PageData, status int) {
	tmpl := c.pageTemplates[pageTemplate]
	if tmpl == nil {
		err := fmt.Errorf("page template %q is not compiled", pageTemplate)
		logging.Error("ui.render", err.Error())
		http.Error(w, "Template rendering error", http.StatusInternalServerError)
		return
	}
	data.BaseURL = c.baseURL
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.Execute(w, data); err != nil {
		logging.Error("ui.render.execute", err.Error())
	}
}

// renderPartial executes a partial compiled once at controller construction.
func (c *UIController) renderPartial(w http.ResponseWriter, partialTemplate string, data any) {
	tmpl := c.partialTemplates[partialTemplate]
	if tmpl == nil {
		err := fmt.Errorf("partial template %q is not compiled", partialTemplate)
		logging.Error("ui.renderPartial", err.Error())
		http.Error(w, "Partial rendering error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		logging.Error("ui.renderPartial.execute", err.Error())
	}
}

// MethodologyViewData holds metadata for /ui/methodology.
type MethodologyViewData struct {
	Expansions []census.ExpansionConfig
}

// Methodology handles GET /ui/methodology.
func (c *UIController) Methodology(w http.ResponseWriter, r *http.Request) {
	var expansions []census.ExpansionConfig
	if c.svc != nil {
		expansions = c.svc.Expansions()
	}
	if len(expansions) == 0 {
		expansions = census.DefaultExpansions
	}

	data := PageData{
		Title:         "Methodology",
		ActiveNav:     "methodology",
		CanonicalPath: "/ui/methodology",
		Description:   "How the FFXIV Census builds its statistics: public Lodestone achievements as the activity signal, snapshot refreshes, data sources, and known limitations.",
		Data: MethodologyViewData{
			Expansions: expansions,
		},
	}
	// The footer shows snapshot freshness on every page. Methodology does not
	// depend on the snapshot, so a missing snapshot never fails this page and
	// its caching headers stay unchanged.
	c.fillFreshness(&data)
	c.render(w, "templates/methodology.html", data)
}

func formatStatsUpdated(state census.UIStatsState) string {
	return state.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC")
}
