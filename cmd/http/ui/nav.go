package ui

// NavItem is one destination in the primary navigation. Key matches the
// ActiveNav a page sets in its PageData.
type NavItem struct {
	Key   string
	Label string
	Href  string
}

// primaryNav lists the primary navigation in the spec's order: Dashboard,
// Worlds, Rankings, Races, Grand Companies, Jobs, Expansions, Methodology.
// A page is linked only once it exists, so each new page adds its own entry
// in its place in that order.
var primaryNav = []NavItem{
	{Key: "dashboard", Label: "Dashboard", Href: "/"},
	{Key: "worlds", Label: "Worlds", Href: "/ui/worlds"},
	{Key: "rankings", Label: "Rankings", Href: "/ui/rankings"},
	{Key: "races", Label: "Races", Href: "/ui/races"},
	{Key: "grand-companies", Label: "Grand Companies", Href: "/ui/grand-companies"},
	{Key: "jobs", Label: "Jobs", Href: "/ui/jobs"},
	{Key: "expansions", Label: "Expansions", Href: "/ui/expansions"},
	{Key: "methodology", Label: "Methodology", Href: "/ui/methodology"},
}
