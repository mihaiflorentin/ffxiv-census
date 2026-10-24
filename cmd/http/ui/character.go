package ui

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// CharacterProfileViewData holds all data for rendering /ui/characters/{id}.
type CharacterProfileViewData struct {
	Character  contract.CharacterRecord
	GenderText string
	IsActive   bool
	JobGroups  []JobGroup
	Milestones []MilestoneDisplay
	MaxLevel   uint32
}

// JobGroup groups jobs by combat/crafting/gathering role.
type JobGroup struct {
	RoleTitle string
	RoleKey   string
	Jobs      []JobDisplay
}

// JobDisplay represents one job card in the profile.
type JobDisplay struct {
	Name     string
	Level    uint8
	ExpLevel uint32
	MaxLevel bool
	RoleKey  string
}

// MilestoneDisplay represents one achieved milestone with detail metadata.
type MilestoneDisplay struct {
	AchievementID uint32
	Kind          string
	Expansion     string
	Detail        string
	AchievedAt    time.Time
}

// CharacterListViewData holds list of characters and pagination for /ui/characters and search results.
type CharacterListViewData struct {
	Title        string
	Query        string
	GrandCompany string
	ActiveOnly   bool
	SortBy       string
	SortOrder    string
	Characters   []CharacterRow
	TotalCount   int64
	CurrentPage  int
	TotalPages   int
	HasPrev      bool
	HasNext      bool
	PrevPage     int
	NextPage     int
}

// CharacterRow represents one character in directory list / search table.
type CharacterRow struct {
	ID              uint32
	Name            string
	World           string
	Datacenter      string
	Region          string
	Race            string
	Tribe           string
	GrandCompany    string
	FreeCompanyName string
	IsActive        bool
	DeletedAt       *time.Time
}

// CharacterDetail handles GET /ui/characters/{id}.
func (c *UIController) CharacterDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	idStr := r.PathValue("id")
	if idStr == "" {
		idStr = r.URL.Query().Get("id")
	}

	id64, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil || id64 == 0 {
		http.Error(w, "Invalid character ID", http.StatusBadRequest)
		return
	}
	charID := uint32(id64)

	var detail *census.CharacterDetail
	if c.svc != nil {
		detail, err = c.svc.CharacterDetail(ctx, charID)
		if err != nil {
			logging.Error("ui.character.detail", err.Error())
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
	}

	if detail == nil {
		w.WriteHeader(http.StatusNotFound)
		c.render(w, "templates/character.html", PageData{
			Title:     "Character Not Found",
			ActiveNav: "characters",
			Data: CharacterProfileViewData{
				Character: contract.CharacterRecord{ID: charID},
			},
		})
		return
	}

	// Gender text
	genderStr := "Unknown"
	switch detail.Character.Gender {
	case 1:
		genderStr = "Male ♂"
	case 2:
		genderStr = "Female ♀"
	}

	// Active status
	isActive := false
	if detail.Character.LatestAchievementAt != nil && c.svc != nil {
		isActive = c.svc.IsActive(*detail.Character.LatestAchievementAt)
	}

	maxLevel := c.maxLevel()

	// Group jobs into 7 buckets
	jobGroups := buildJobGroups(detail.Jobs, maxLevel)

	// Map milestones
	var allMilestones []contract.MilestoneAchievement
	if c.svc != nil {
		allMilestones = c.svc.Milestones()
	}
	if len(allMilestones) == 0 {
		allMilestones = census.DefaultMilestones()
	}

	milestoneMap := make(map[uint32]contract.MilestoneAchievement)
	for _, m := range allMilestones {
		milestoneMap[m.AchievementID] = m
	}
	var milestones []MilestoneDisplay
	for _, cm := range detail.Milestones {
		meta, ok := milestoneMap[cm.AchievementID]
		exp := ""
		detailText := ""
		kind := ""
		if ok {
			kind = meta.Kind
			if meta.Expansion != nil {
				exp = *meta.Expansion
			}
			detailText = meta.Detail
		}
		milestones = append(milestones, MilestoneDisplay{
			AchievementID: cm.AchievementID,
			Kind:          kind,
			Expansion:     exp,
			Detail:        detailText,
			AchievedAt:    cm.AchievedAt,
		})
	}

	// Sort milestones newest first
	sort.Slice(milestones, func(i, j int) bool {
		return milestones[i].AchievedAt.After(milestones[j].AchievedAt)
	})

	viewData := CharacterProfileViewData{
		Character:  detail.Character,
		GenderText: genderStr,
		IsActive:   isActive,
		JobGroups:  jobGroups,
		Milestones: milestones,
		MaxLevel:   maxLevel,
	}

	c.render(w, "templates/character.html", PageData{
		Title:     detail.Character.Name,
		ActiveNav: "characters",
		Data:      viewData,

		ActivityWindow: Window{Days: census.WindowDays(c.svc.ActivityWindow())},
	})
}

// CharacterSearch handles GET /ui/characters/search?q=...
func (c *UIController) CharacterSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		http.Redirect(w, r, "/ui/characters", http.StatusFound)
		return
	}

	// If numeric query, redirect directly to profile
	if _, err := strconv.ParseUint(query, 10, 32); err == nil {
		http.Redirect(w, r, "/ui/characters/"+query, http.StatusFound)
		return
	}

	// Forward search request internally to CharacterList
	c.CharacterList(w, r)
}

// CharacterList handles GET /ui/characters.
func (c *UIController) CharacterList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	page := 1
	if pStr := query.Get("page"); pStr != "" {
		if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
			page = p
		}
	}

	limit := 30
	offset := (page - 1) * limit

	qName := strings.TrimSpace(query.Get("q"))
	if qName == "" {
		qName = strings.TrimSpace(query.Get("name"))
	}
	grandCompany := strings.TrimSpace(query.Get("grand_company"))
	activeOnly := query.Get("active") == "true" || query.Get("active") == "1"
	sortBy := query.Get("sort_by")
	sortOrder := query.Get("sort_order")

	filter := contract.CharacterFilter{
		Name:         qName,
		GrandCompany: grandCompany,
		ActiveOnly:   activeOnly,
		SortBy:       sortBy,
		SortOrder:    sortOrder,
	}
	if activeOnly && c.svc != nil {
		since := c.svc.ActivitySince()
		filter.Since = &since
	}

	var chars []contract.CharacterRecord
	var total int64
	var err error

	if c.svc != nil {
		chars, total, err = c.svc.ListCharacters(ctx, filter, limit, offset)
		if err != nil {
			logging.Error("ui.character.list", err.Error())
		}
	}

	totalPages := int((total + int64(limit) - 1) / int64(limit))
	if totalPages == 0 {
		totalPages = 1
	}

	var rows []CharacterRow
	for _, ch := range chars {
		isActive := false
		if ch.LatestAchievementAt != nil && c.svc != nil {
			isActive = c.svc.IsActive(*ch.LatestAchievementAt)
		}

		fcName := ""
		if ch.FreeCompanyName != nil {
			fcName = *ch.FreeCompanyName
		}

		rows = append(rows, CharacterRow{
			ID:              ch.ID,
			Name:            ch.Name,
			World:           ch.World,
			Datacenter:      ch.Datacenter,
			Region:          ch.Region,
			Race:            ch.Race,
			Tribe:           ch.Tribe,
			GrandCompany:    ch.GrandCompany,
			FreeCompanyName: fcName,
			IsActive:        isActive,
			DeletedAt:       ch.DeletedAt,
		})
	}

	title := "Character Directory"
	if qName != "" {
		title = fmt.Sprintf("Search: %q", qName)
	}

	c.render(w, "templates/characters_list.html", PageData{
		Title:       title,
		ActiveNav:   "characters",
		SearchQuery: qName,
		Data: CharacterListViewData{
			Title:        title,
			Query:        qName,
			GrandCompany: grandCompany,
			ActiveOnly:   activeOnly,
			SortBy:       sortBy,
			SortOrder:    sortOrder,
			Characters:   rows,
			TotalCount:   total,
			CurrentPage:  page,
			TotalPages:   totalPages,
			HasPrev:      page > 1,
			HasNext:      page < totalPages,
			PrevPage:     page - 1,
			NextPage:     page + 1,
		},
	})
}

// buildJobGroups classifies and sorts character jobs into 7 standard categories.
func buildJobGroups(jobs []contract.ClassJobRecord, maxLevel uint32) []JobGroup {
	jobMap := make(map[string]contract.ClassJobRecord)
	for _, j := range jobs {
		jobMap[strings.TrimSpace(j.Name)] = j
	}

	categories := []struct {
		Title   string
		RoleKey string
		Jobs    []string
	}{
		{
			Title:   "Tank",
			RoleKey: "tank",
			Jobs:    []string{"Paladin", "Warrior", "Dark Knight", "Gunbreaker"},
		},
		{
			Title:   "Healer",
			RoleKey: "healer",
			Jobs:    []string{"White Mage", "Scholar", "Astrologian", "Sage"},
		},
		{
			Title:   "Melee DPS",
			RoleKey: "melee",
			Jobs:    []string{"Monk", "Dragoon", "Ninja", "Samurai", "Reaper", "Viper"},
		},
		{
			Title:   "Physical Ranged DPS",
			RoleKey: "phys_ranged",
			Jobs:    []string{"Bard", "Machinist", "Dancer"},
		},
		{
			Title:   "Magic Ranged DPS",
			RoleKey: "magic_ranged",
			Jobs:    []string{"Black Mage", "Summoner", "Red Mage", "Pictomancer", "Blue Mage"},
		},
		{
			Title:   "Disciples of the Hand (Crafters)",
			RoleKey: "crafter",
			Jobs:    []string{"Carpenter", "Blacksmith", "Armorer", "Goldsmith", "Leatherworker", "Weaver", "Alchemist", "Culinarian"},
		},
		{
			Title:   "Disciples of the Land (Gatherers)",
			RoleKey: "gatherer",
			Jobs:    []string{"Miner", "Botanist", "Fisher"},
		},
	}

	var groups []JobGroup
	for _, cat := range categories {
		var jobDisplays []JobDisplay
		for _, name := range cat.Jobs {
			rec, ok := jobMap[name]
			var lvl uint8
			var exp uint32
			if ok {
				lvl = rec.Level
				exp = rec.ExpLevel
			}
			jobDisplays = append(jobDisplays, JobDisplay{
				Name:     name,
				Level:    lvl,
				ExpLevel: exp,
				MaxLevel: uint32(lvl) >= maxLevel && lvl > 0,
				RoleKey:  cat.RoleKey,
			})
		}
		groups = append(groups, JobGroup{
			RoleTitle: cat.Title,
			RoleKey:   cat.RoleKey,
			Jobs:      jobDisplays,
		})
	}

	return groups
}
