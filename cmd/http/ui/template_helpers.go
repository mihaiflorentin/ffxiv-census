package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// formatNumber adds commas to integer values (e.g. 1234567 -> "1,234,567").
func formatNumber(v any) string {
	var n int64
	switch val := v.(type) {
	case int:
		n = int64(val)
	case int64:
		n = val
	case int32:
		n = int64(val)
	case uint:
		n = int64(val)
	case uint32:
		n = int64(val)
	case uint64:
		n = int64(val)
	default:
		return "0"
	}

	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return sign + s
	}

	var parts []string
	lead := len(s) % 3
	if lead > 0 {
		parts = append(parts, s[:lead])
	}
	for i := lead; i < len(s); i += 3 {
		parts = append(parts, s[i:i+3])
	}
	return sign + strings.Join(parts, ",")
}

// formatPercent calculates numerator / denominator * 100 with one decimal place.
func formatPercent(num, denom int64) string {
	if denom == 0 {
		return "0.0%"
	}
	pct := (float64(num) / float64(denom)) * 100
	return fmt.Sprintf("%.1f%%", pct)
}

// formatTrend renders the change between two window totals as an arrow with
// the absolute delta and the percentage of the previous value. A zero
// previous window renders the absolute delta only; two zero windows render
// an empty string; equal non-zero windows render without an arrow.
func formatTrend(current, previous int64) string {
	delta := current - previous
	if delta == 0 && previous == 0 {
		return ""
	}
	if delta == 0 {
		return fmt.Sprintf("%s (%s)", formatNumber(0), formatPercent(0, previous))
	}
	arrow := "▲"
	if delta < 0 {
		arrow = "▼"
	}
	absolute := delta
	if absolute < 0 {
		absolute = -absolute
	}
	if previous == 0 {
		return fmt.Sprintf("%s %s", arrow, formatNumber(absolute))
	}
	return fmt.Sprintf("%s %s (%s)", arrow, formatNumber(absolute), formatPercent(absolute, previous))
}

// formatDate formats a time.Time or *time.Time as "YYYY-MM-DD" or returns "-" if nil/zero.
func formatDate(v any) string {
	if v == nil {
		return "-"
	}
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02")
	case *time.Time:
		if t == nil || t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02")
	default:
		return "-"
	}
}

// formatDateTime formats a time.Time or *time.Time as "YYYY-MM-DD 15:04" or returns "-" if nil/zero.
func formatDateTime(v any) string {
	if v == nil {
		return "-"
	}
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04")
	case *time.Time:
		if t == nil || t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04")
	default:
		return "-"
	}
}

// jobCategory classifies FFXIV class/job names into combat/crafting/gathering roles.
func jobCategory(jobName string) string {
	switch strings.TrimSpace(jobName) {
	case "Paladin", "Gladiator", "Warrior", "Marauder", "Dark Knight", "Gunbreaker":
		return "tank"
	case "White Mage", "Conjurer", "Scholar", "Astrologian", "Sage":
		return "healer"
	case "Monk", "Pugilist", "Dragoon", "Lancer", "Ninja", "Rogue", "Samurai", "Reaper", "Viper":
		return "melee"
	case "Bard", "Archer", "Machinist", "Dancer":
		return "phys_ranged"
	case "Black Mage", "Thaumaturge", "Summoner", "Arcanist", "Red Mage", "Pictomancer", "Blue Mage":
		return "magic_ranged"
	case "Carpenter", "Blacksmith", "Armorer", "Goldsmith", "Leatherworker", "Weaver", "Alchemist", "Culinarian":
		return "crafter"
	case "Miner", "Botanist", "Fisher":
		return "gatherer"
	default:
		return "other"
	}
}

// roleColor returns the CSS class for a given job category.
func roleColor(role string) string {
	switch role {
	case "tank":
		return "role-tank"
	case "healer":
		return "role-healer"
	case "melee":
		return "role-melee"
	case "phys_ranged":
		return "role-phys-ranged"
	case "magic_ranged":
		return "role-magic-ranged"
	case "crafter":
		return "role-crafter"
	case "gatherer":
		return "role-gatherer"
	default:
		return "role-other"
	}
}

// templateFuncs is the standard FuncMap registered across all UI templates.
var templateFuncs = template.FuncMap{
	"emptyState":     emptyState,
	"emptyStateLink": emptyStateLink,
	"formatNumber":   formatNumber,
	"regionName":     regionDisplayName,
	"formatPercent":  formatPercent,
	"formatDate":     formatDate,
	"formatDateTime": formatDateTime,
	"jobCategory":    jobCategory,
	"roleColor":      roleColor,
	"add": func(a, b int) int {
		return a + b
	},
	"sub": func(a, b int) int {
		return a - b
	},
	"mul": func(a, b int) int {
		return a * b
	},
	"div": func(a, b int) int {
		if b == 0 {
			return 0
		}
		return a / b
	},
	"safeHTML": func(s string) template.HTML {
		return template.HTML(s)
	},
	"jsonSafe": func(v any) template.JS {
		b, err := json.Marshal(v)
		if err != nil {
			return template.JS("{}")
		}
		return template.JS(b)
	},
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 {
			return nil, fmt.Errorf("invalid dict call: uneven number of arguments")
		}
		d := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings")
			}
			d[key] = values[i+1]
		}
		return d, nil
	},
}

// EmptyState is the argument of the shared "empty-state" template component.
// Title says what is missing; Hint says what happens next or what to do.
type EmptyState struct {
	Title  string
	Hint   string
	Href   string
	Action string
}

func emptyState(title, hint string) EmptyState {
	return EmptyState{Title: title, Hint: hint}
}

func emptyStateLink(title, hint, href, action string) EmptyState {
	return EmptyState{Title: title, Hint: hint, Href: href, Action: action}
}
