package ui

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
)

// placeFilter is the region / datacenter / world selection of a request,
// resolved once to canonical names ("na" -> "NA", "chaos" -> "Chaos") so the
// figures, pills, selects, chips and scope text all agree. Unknown values
// pass through unchanged.
type placeFilter struct {
	Region     string
	Datacenter string
	World      string
}

func parsePlaceFilter(r *http.Request) placeFilter {
	q := r.URL.Query()
	return placeFilter{
		Region:     census.CanonicalRegion(strings.TrimSpace(q.Get("region"))),
		Datacenter: census.CanonicalDatacenter(strings.TrimSpace(q.Get("dc"))),
		World:      census.CanonicalWorld(strings.TrimSpace(q.Get("world"))),
	}
}

// FilterBar is the view model of the shared filter bar used by the Races and
// Worlds pages (templates/components/filters.html). Every control carries a
// visible label; the query parameters and their order are owned by the page.
type FilterBar struct {
	// Action is the form target, e.g. "/ui/races".
	Action string
	// Label names the filter form for assistive technology.
	Label string
	// Pills is an optional group of link pills (Worlds' region selector).
	Pills *FilterPills
	// Hidden carries parameters a select form must keep on submit.
	Hidden []FilterHidden
	// Selects are auto-submitting dropdowns, in cascade order.
	Selects []FilterSelect
	// Chips list the applied filters, each linking to the page without it.
	Chips []FilterChip
	// ClearHref is the unfiltered page, offered when more than one filter
	// is applied.
	ClearHref string
}

// FilterChip is one applied filter with a link that removes only it.
type FilterChip struct {
	Label      string
	Value      string
	RemoveHref string
}

// appliedFilter is one query parameter of a filtered page, in URL order.
type appliedFilter struct {
	Name  string // query parameter
	Label string // visible filter name
	Value string // raw parameter value; empty when not applied
	Shown string // value as displayed; defaults to Value
}

// filterChips returns one chip per applied filter. Each chip links to path
// with every other applied filter kept, in the same parameter order.
func filterChips(path string, filters []appliedFilter) []FilterChip {
	var chips []FilterChip
	for i, f := range filters {
		if f.Value == "" {
			continue
		}
		var query []string
		for j, other := range filters {
			if j != i && other.Value != "" {
				query = append(query, other.Name+"="+url.QueryEscape(other.Value))
			}
		}
		href := path
		if len(query) > 0 {
			href += "?" + strings.Join(query, "&")
		}
		shown := f.Shown
		if shown == "" {
			shown = f.Value
		}
		chips = append(chips, FilterChip{Label: f.Label, Value: shown, RemoveHref: href})
	}
	return chips
}

// FilterPills is a labelled group of mutually exclusive filter links.
type FilterPills struct {
	ID      string
	Label   string
	Options []FilterLink
}

// FilterLink is one pill: a link to the page with that option applied.
type FilterLink struct {
	Label   string
	Href    string
	Current bool
}

// FilterHidden is a hidden form field.
type FilterHidden struct {
	Name  string
	Value string
}

// FilterSelect is one labelled dropdown.
type FilterSelect struct {
	ID       string
	Name     string
	Label    string
	AllLabel string
	Options  []FilterOption
}

// FilterOption is one dropdown option; Label defaults to Value when empty.
type FilterOption struct {
	Value    string
	Label    string
	Selected bool
}

// filterOptions builds dropdown options, marking selected and labelling each
// value with label(value) when label is non-nil.
func filterOptions(values []string, selected string, label func(string) string) []FilterOption {
	out := make([]FilterOption, 0, len(values))
	for _, v := range values {
		opt := FilterOption{Value: v, Label: v, Selected: v == selected}
		if label != nil {
			opt.Label = label(v)
		}
		out = append(out, opt)
	}
	return out
}
