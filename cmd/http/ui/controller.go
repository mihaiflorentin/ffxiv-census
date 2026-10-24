package ui

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"net/http"
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
	}
	for _, page := range []string{
		"templates/dashboard.html",
		"templates/races.html",
		"templates/worlds.html",
		"templates/world_detail.html",
		"templates/expansions.html",
		"templates/methodology.html",
		"templates/character.html",
		"templates/characters_list.html",
	} {
		controller.pageTemplates[page] = template.Must(template.New("layout.html").Funcs(templateFuncs).ParseFS(content, "templates/layout.html", page))
	}
	for _, partial := range []string{
		"templates/partials/world_drilldown.html",
		"templates/partials/search_results.html",
	} {
		parsed := template.Must(template.New("partial").Funcs(templateFuncs).ParseFS(content, partial))
		for _, candidate := range parsed.Templates() {
			if candidate.Name() != "partial" {
				controller.partialTemplates[partial] = candidate
				break
			}
		}
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
	Data          any
}

func (c *UIController) currentStats(w http.ResponseWriter, r *http.Request) (*contract.UIStatsSnapshot, census.UIStatsState, bool) {
	if c.stats == nil {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Statistics are temporarily unavailable. Please retry shortly.", http.StatusServiceUnavailable)
		return nil, census.UIStatsState{}, false
	}
	snapshot, state, err := c.stats.Current(r.Context())
	if err != nil {
		logging.Error("ui.stats", err.Error())
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Statistics are temporarily unavailable. Please retry shortly.", http.StatusServiceUnavailable)
		return nil, census.UIStatsState{}, false
	}

	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("Vary", "HX-Request, Accept-Encoding")
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%s|%s|%s", snapshot.GeneratedAt.UnixNano(), r.URL.Path, r.URL.Query().Encode(), strings.ToLower(r.Header.Get("HX-Request")))
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

// render executes a template compiled once at controller construction.
func (c *UIController) render(w http.ResponseWriter, pageTemplate string, data PageData) {
	tmpl := c.pageTemplates[pageTemplate]
	if tmpl == nil {
		err := fmt.Errorf("page template %q is not compiled", pageTemplate)
		logging.Error("ui.render", err.Error())
		http.Error(w, "Template rendering error", http.StatusInternalServerError)
		return
	}
	data.BaseURL = c.baseURL
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	if c.stats != nil {
		if _, state, err := c.stats.Current(r.Context()); err == nil {
			data.StatsUpdated = formatStatsUpdated(state)
			data.StatsStale = state.Stale
		}
	}
	c.render(w, "templates/methodology.html", data)
}

func formatStatsUpdated(state census.UIStatsState) string {
	return state.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC")
}
