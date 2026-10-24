package ui

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
)

// Switch feeds the shared segmented "switch" component: a small set of
// mutually exclusive views of one region (usually a link pair). Each option
// is a plain link, so the switch works without JavaScript; with HTMX the link
// swaps only the region and pushes its URL.
//
// The region the switch swaps is a page template block whose root element
// carries id=Target, data-switch-announce (what a screen reader hears after
// the swap) and one heading with data-switch-heading and tabindex="-1" (where
// focus lands). Put the switch inside that region so its current option is
// re-rendered by the swap. See the Worlds rankings for the pattern.
type Switch struct {
	// Label names the choice for assistive technology ("Rank worlds by").
	Label string
	// Target is the id of the region the switch swaps.
	Target  string
	Options []SwitchOption
}

// SwitchOption is one choice of a Switch.
type SwitchOption struct {
	Label   string
	Href    string
	Current bool
}

// wantsSwitchRegion reports whether r is an HTMX switch request for the region
// with the given id. History-restore requests (the back button after a cache
// miss) need the full page, so they never count.
func wantsSwitchRegion(r *http.Request, regionID string) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true") &&
		!strings.EqualFold(r.Header.Get("HX-History-Restore-Request"), "true") &&
		r.Header.Get("HX-Target") == regionID
}

// renderRegion executes one named block of a precompiled page template as an
// HTMX fragment.
func (c *UIController) renderRegion(w http.ResponseWriter, pageTemplate, block string, data PageData) {
	tmpl := c.pageTemplates[pageTemplate]
	if tmpl == nil || tmpl.Lookup(block) == nil {
		err := fmt.Errorf("region %q of page %q is not compiled", block, pageTemplate)
		logging.Error("ui.renderRegion", err.Error())
		http.Error(w, "Template rendering error", http.StatusInternalServerError)
		return
	}
	data.BaseURL = c.baseURL
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, block, data); err != nil {
		logging.Error("ui.renderRegion.execute", err.Error())
		http.Error(w, "Template rendering error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out.Bytes())
}
