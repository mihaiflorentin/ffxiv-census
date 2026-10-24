package ui

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// TestPreloadedFontsServedAsWOFF2 verifies every font the shell preloads is
// served by the asset route as WOFF2 with long-lived caching.
func TestPreloadedFontsServedAsWOFF2(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	doc := parseHTML(t, serveMuxGet(t, mux, "/").Body.String())
	preloads := findAll(doc, func(n *html.Node) bool {
		rel, _ := attr(n, "rel")
		as, _ := attr(n, "as")
		return n.DataAtom == atom.Link && rel == "preload" && as == "font"
	})
	if len(preloads) == 0 {
		t.Fatal("expected the shell to preload at least one font")
	}
	for _, link := range preloads {
		href, _ := attr(link, "href")
		if _, ok := attr(link, "crossorigin"); !ok {
			t.Errorf("font preload %q must be crossorigin or browsers fetch it twice", href)
		}
		rec := serveMuxGet(t, mux, href)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d", href, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("GET %s: Content-Type %q, want font/woff2", href, ct)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "max-age=31536000") || !strings.Contains(cc, "immutable") {
			t.Errorf("GET %s: Cache-Control %q, want a year-long immutable cache", href, cc)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), []byte("wOF2")) {
			t.Errorf("GET %s: body is not a WOFF2 file", href)
		}
	}
}

// TestStylesheetServedAsCSS verifies the linked stylesheet is served as CSS
// and that its font faces resolve to fonts the asset route serves.
func TestStylesheetServedAsCSS(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	rec := serveMuxGet(t, mux, "/ui/assets/styles.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET styles.css: status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("styles.css Content-Type %q, want text/css", ct)
	}
	if !strings.Contains(rec.Body.String(), "@font-face") {
		t.Fatal("stylesheet must declare the vendored font face")
	}
	for _, u := range cssURLs(rec.Body.String()) {
		target := resolveAssetURL(u)
		if got := serveMuxGet(t, mux, target); got.Code != http.StatusOK {
			t.Errorf("stylesheet url(%s) -> %s: status %d", u, target, got.Code)
		}
	}
}

var cssURLPattern = regexp.MustCompile(`url\(\s*['"]?([^'")]+)['"]?\s*\)`)

func cssURLs(css string) []string {
	var out []string
	for _, m := range cssURLPattern.FindAllStringSubmatch(css, -1) {
		out = append(out, m[1])
	}
	return out
}

// resolveAssetURL resolves a stylesheet-relative URL against /ui/assets/.
func resolveAssetURL(u string) string {
	if strings.HasPrefix(u, "/") {
		return u
	}
	return "/ui/assets/" + strings.TrimPrefix(u, "./")
}

// allowedExternalHref reports whether an absolute link is one of the
// deliberate off-site references: canonical/OG URLs on the site's own base
// URL and the ffxivbard.com footer link.
func allowedExternalHref(n *html.Node, href string) bool {
	if rel, _ := attr(n, "rel"); rel == "canonical" {
		return true
	}
	if strings.HasPrefix(href, testBaseURL) {
		return true
	}
	return n.DataAtom == atom.A && (href == "https://ffxivbard.com" || strings.HasPrefix(href, "https://ffxivbard.com/"))
}

func isExternal(u string) bool {
	lower := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "//")
}

// TestNoPageReferencesExternalHosts turns the no-CDN rule into a check: no
// rendered page or the stylesheet it links pulls anything from another host.
func TestNoPageReferencesExternalHosts(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	for _, tc := range shellPages {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseHTML(t, serveMuxGet(t, mux, tc.path).Body.String())
			for _, n := range findAll(doc, func(*html.Node) bool { return true }) {
				for _, key := range []string{"src", "href", "srcset", "poster", "action"} {
					v, ok := attr(n, key)
					if !ok || !isExternal(v) {
						continue
					}
					if key == "href" && allowedExternalHref(n, v) {
						continue
					}
					t.Errorf("<%s %s=%q> references an external host", n.Data, key, v)
				}
				if n.DataAtom == atom.Style {
					for _, u := range cssURLs(textOf(n)) {
						if isExternal(u) {
							t.Errorf("inline <style> url(%s) references an external host", u)
						}
					}
				}
				if style, ok := attr(n, "style"); ok {
					for _, u := range cssURLs(style) {
						if isExternal(u) {
							t.Errorf("<%s style> url(%s) references an external host", n.Data, u)
						}
					}
				}
				if rel, _ := attr(n, "rel"); n.DataAtom == atom.Link && rel == "stylesheet" {
					href, _ := attr(n, "href")
					css := serveMuxGet(t, mux, href).Body.String()
					if strings.Contains(css, "@import") {
						t.Errorf("stylesheet %s uses @import", href)
					}
					for _, u := range cssURLs(css) {
						if isExternal(u) {
							t.Errorf("stylesheet %s url(%s) references an external host", href, u)
						}
					}
				}
			}
		})
	}
}

// TestPagesLinkVersionedStylesAndScripts stops a cache (the browser or the
// CDN) serving the previous release's CSS or JS with the new markup: every
// stylesheet and script URL carries the build fingerprint, so each release
// gets new URLs.
func TestPagesLinkVersionedStylesAndScripts(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	body := serveMuxGet(t, mux, "/ui/methodology").Body.String()
	want := "?v=" + uiBuildID()
	for _, name := range []string{"styles.css", "htmx.min.js", "chart.umd.min.js", "chart-theme.js", "census.js"} {
		if !strings.Contains(body, "/ui/assets/"+name+want) {
			t.Errorf("page links /ui/assets/%s without the build version %q", name, want)
		}
	}
}

// TestVersionedAssetsAreImmutable lets caches keep a versioned file for a
// year, while an unversioned or out-of-date URL is only cached briefly.
func TestVersionedAssetsAreImmutable(t *testing.T) {
	rig := newTestRig(t)
	mux := http.NewServeMux()
	rig.ctrl.RegisterRoutes(mux)

	current := serveMuxGet(t, mux, "/ui/assets/styles.css?v="+uiBuildID())
	if got := current.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("current version Cache-Control = %q, want a year, immutable", got)
	}
	for _, target := range []string{"/ui/assets/styles.css", "/ui/assets/styles.css?v=old-build"} {
		if got := serveMuxGet(t, mux, target).Header().Get("Cache-Control"); got != "public, max-age=60" {
			t.Errorf("%s Cache-Control = %q, want a short cache", target, got)
		}
	}
}
