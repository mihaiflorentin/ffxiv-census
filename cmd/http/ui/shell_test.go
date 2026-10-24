package ui

import (
	"net/http"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// shellPages lists every indexable page with the primary-nav destination that
// must be marked as the current page when it renders.
var shellPages = []struct {
	name       string
	path       string
	currentNav string
}{
	{"Dashboard At Root", "/", "/"},
	{"Dashboard", "/ui/dashboard", "/"},
	{"Races", "/ui/races", "/ui/races"},
	{"Grand Companies", "/ui/grand-companies", "/ui/grand-companies"},
	{"Worlds", "/ui/worlds", "/ui/worlds"},
	{"World Detail", "/ui/worlds/Adamantoise", "/ui/worlds"},
	{"Expansions", "/ui/expansions", "/ui/expansions"},
	{"Methodology", "/ui/methodology", "/ui/methodology"},
}

func parseHTML(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse rendered HTML: %v", err)
	}
	return doc
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// findAll returns every element under root (inclusive) that matches pred.
func findAll(root *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && pred(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func byAtom(a atom.Atom) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.DataAtom == a }
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func containsEmoji(s string) bool {
	for _, r := range s {
		if r >= 0x1F000 || (r >= 0x2600 && r <= 0x27BF) || r == 0xFE0F {
			return true
		}
		if unicode.Is(unicode.So, r) {
			return true
		}
	}
	return false
}

// TestPageShellLandmarks verifies every page renders inside the shared shell:
// a skip link to the main landmark, the brand wordmark linking home, primary
// navigation marking the current page, a single h1 and the footer.
func TestPageShellLandmarks(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	for _, tc := range shellPages {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveMuxGet(t, mux, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}
			doc := parseHTML(t, rec.Body.String())

			// Skip link: the first link in the body targets the main landmark.
			links := findAll(doc, byAtom(atom.A))
			if len(links) == 0 {
				t.Fatal("no links rendered")
			}
			skipHref, _ := attr(links[0], "href")
			if !strings.HasPrefix(skipHref, "#") || !strings.Contains(strings.ToLower(textOf(links[0])), "skip") {
				t.Fatalf("first link must be a skip link to an in-page target, got href=%q text=%q", skipHref, textOf(links[0]))
			}
			mains := findAll(doc, byAtom(atom.Main))
			if len(mains) != 1 {
				t.Fatalf("expected exactly one main landmark, got %d", len(mains))
			}
			if id, _ := attr(mains[0], "id"); "#"+id != skipHref {
				t.Errorf("skip link targets %q but main landmark id is %q", skipHref, id)
			}

			// Brand: header link home with the wordmark and a vector mark.
			headers := findAll(doc, byAtom(atom.Header))
			if len(headers) == 0 {
				t.Fatal("no header landmark rendered")
			}
			header := headers[0]
			brand := findAll(header, func(n *html.Node) bool {
				href, _ := attr(n, "href")
				return n.DataAtom == atom.A && href == "/" && strings.Contains(textOf(n), "FFXIV Census")
			})
			if len(brand) != 1 {
				t.Fatalf("expected one brand link home with wordmark 'FFXIV Census', got %d", len(brand))
			}
			if len(findAll(brand[0], byAtom(atom.Svg))) == 0 {
				t.Error("brand link must carry an inline SVG mark")
			}

			// Primary navigation with exactly one current page.
			navs := findAll(header, func(n *html.Node) bool {
				label, _ := attr(n, "aria-label")
				return n.DataAtom == atom.Nav && strings.EqualFold(label, "Primary")
			})
			if len(navs) != 1 {
				t.Fatalf("expected one primary nav in header, got %d", len(navs))
			}
			var navOrder []string
			var current []string
			for _, a := range findAll(navs[0], byAtom(atom.A)) {
				href, _ := attr(a, "href")
				navOrder = append(navOrder, href)
				if v, ok := attr(a, "aria-current"); ok && v == "page" {
					current = append(current, href)
				}
			}
			// The spec's order is Dashboard, Worlds, Rankings, Races, Grand
			// Companies, Jobs, Expansions, Methodology; a page is linked only
			// once it exists.
			if want := []string{"/", "/ui/worlds", "/ui/races", "/ui/grand-companies", "/ui/expansions", "/ui/methodology"}; strings.Join(navOrder, " ") != strings.Join(want, " ") {
				t.Errorf("primary nav links %v, want %v in that order", navOrder, want)
			}
			if len(current) != 1 || current[0] != tc.currentNav {
				t.Errorf("aria-current=page got %v, want only %q", current, tc.currentNav)
			}

			// Exactly one h1.
			if h1s := findAll(doc, byAtom(atom.H1)); len(h1s) != 1 {
				t.Errorf("expected exactly one h1, got %d", len(h1s))
			}

			// Footer: freshness, Methodology link and ffxivbard.com link.
			footers := findAll(doc, byAtom(atom.Footer))
			if len(footers) != 1 {
				t.Fatalf("expected one footer, got %d", len(footers))
			}
			footer := footers[0]
			if !strings.Contains(textOf(footer), "Statistics updated") {
				t.Errorf("footer must show snapshot freshness, got %q", textOf(footer))
			}
			var hasMethodology, hasBard bool
			for _, a := range findAll(footer, byAtom(atom.A)) {
				href, _ := attr(a, "href")
				switch {
				case href == "/ui/methodology":
					hasMethodology = true
				case strings.HasPrefix(href, "https://ffxivbard.com"):
					hasBard = true
				}
			}
			if !hasMethodology || !hasBard {
				t.Errorf("footer links: methodology=%v ffxivbard.com=%v, want both", hasMethodology, hasBard)
			}

			// The shell itself carries no inline styles or emoji.
			for _, region := range []*html.Node{header, footer, links[0]} {
				if styled := findAll(region, func(n *html.Node) bool { _, ok := attr(n, "style"); return ok }); len(styled) > 0 {
					t.Errorf("shell element <%s> contains inline style attributes", region.Data)
				}
				if containsEmoji(textOf(region)) {
					t.Errorf("shell element <%s> contains emoji: %q", region.Data, textOf(region))
				}
			}
		})
	}
}
