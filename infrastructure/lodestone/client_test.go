package lodestone

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/config"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/httpclient"
	"github.com/mihaiflorentin88/ffxiv-census/mock"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
	"golang.org/x/time/rate"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCustomClient(rt http.RoundTripper) *CustomClient {
	return &CustomClient{httpClient: &http.Client{Transport: rt}, limiter: rate.NewLimiter(rate.Inf, 1), logger: loggerOrDiscard(nil)}
}

func TestCustomClient_FetchAchievements_403MarksPrivateWithOneRequest(t *testing.T) {
	requests := 0
	c := testCustomClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/lodestone/character/1/achievement/" {
			t.Fatalf("path = %s, want list privacy/latest request", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	}))
	summary, err := c.FetchAchievements(context.Background(), 1, []uint32{590, 1129})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests)
	}
	if summary == nil || !summary.Private {
		t.Fatalf("summary = %+v, want private", summary)
	}
}

func TestCustomClient_FetchAchievements_FetchesListBeforeMissingDetails(t *testing.T) {
	var paths []string
	c := testCustomClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/lodestone/character/1/achievement/" {
			html := `<li class="entry"><a href="/lodestone/character/1/achievement/detail/999/" class="entry__achievement"><div class="entry__achievement--list entry__achievement--history"><time class="entry__activity__time"><script>ldst_strftime(1700000000, 'YMD')</script></time><p class="entry__activity__txt">Character achievement "Newest" earned!</p></div></a></li>`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(html)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<div class="entry__achievement__view"></div>`)), Header: make(http.Header)}, nil
	}))
	summary, err := c.FetchAchievements(context.Background(), 1, []uint32{590})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/lodestone/character/1/achievement/" || paths[1] != "/lodestone/character/1/achievement/detail/590/" {
		t.Fatalf("paths = %v, want list then one detail", paths)
	}
	if summary.LatestAchievement == nil || summary.LatestAchievement.AchievementID != 999 || summary.LatestAchievement.Name != "Newest" || summary.LatestAchievement.EarnedAt.Unix() != 1700000000 {
		t.Fatalf("latest = %+v, want list item 999", summary.LatestAchievement)
	}
}

func TestCustomClient_FetchAchievements_AllKnownStillFetchesList(t *testing.T) {
	requests := 0
	c := testCustomClient(roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/lodestone/character/1/achievement/" {
			t.Fatalf("path = %s, want list", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<div></div>`)), Header: make(http.Header)}, nil
	}))
	if _, err := c.FetchAchievements(context.Background(), 1, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestCustomClient_FetchAchievements_200IncompleteMeansUnearned(t *testing.T) {
	requests := 0
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		html := `<div class="entry__achievement__view"><p class="entry__activity__txt">Not yet</p></div>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(html)), Header: make(http.Header)}, nil
	}))
	summary, err := c.FetchAchievements(context.Background(), 1, []uint32{590, 1129})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (list plus first detail)", requests)
	}
	if summary == nil || summary.Private || len(summary.Milestones) != 0 {
		t.Fatalf("summary = %+v, want public unearned", summary)
	}
}

func TestCheckSingleAchievement_CompleteWithoutDateReturnsError(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`<div class="entry__achievement__view entry__achievement__view--complete"><time class="entry__activity__time"></time></div>`)), Header: make(http.Header)}, nil
	}))
	if _, err := c.checkSingleAchievement(context.Background(), 1, 590); err == nil {
		t.Fatal("expected missing timestamp error")
	}
}

func TestCheckSingleAchievement_CompleteDoesNotUseLaterPageTimestamp(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		html := `<div class="entry__achievement__view entry__achievement__view--complete"></div><time class="entry__activity__time"><script>ldst_strftime(1486702764, 'YMD')</script></time>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(html)), Header: make(http.Header)}, nil
	}))
	if _, err := c.checkSingleAchievement(context.Background(), 1, 590); err == nil {
		t.Fatal("expected missing row timestamp error")
	}
}

func TestCheckSingleAchievement_OverflowEpochReturnsError(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		html := `<div class="entry__achievement__view entry__achievement__view--complete"><time class="entry__activity__time"><script>ldst_strftime(999999999999999999999, 'YMD')</script></time></div>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(html)), Header: make(http.Header)}, nil
	}))
	if _, err := c.checkSingleAchievement(context.Background(), 1, 590); err == nil {
		t.Fatal("expected overflow timestamp error")
	}
}

func TestExtractAchievementTimestamp_IgnoresGlobalPageDates(t *testing.T) {
	html := `<script>ldst_strftime(1785225600, 'YMD')</script><div class="entry__achievement__view entry__achievement__view--complete"><time class="entry__activity__time"><script>ldst_strftime(1486702764, 'YMD')</script></time></div><script>ldst_strftime(1777363200, 'YMD')</script>`
	got, err := extractAchievementTimestamp(html)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1486702764, 0).UTC(); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExtractAchievementTimestamp_HandlesNestedCompletedRow(t *testing.T) {
	html := `<script>ldst_strftime(1785225600, 'YMD')</script>
		<div class="entry__achievement__view entry__achievement__view--complete">
			<div class="entry__achievement__item"><div class="entry__achievement__frame"><img></div></div>
			<div class="entry__achievement--list entry__achievement--history">
				<p class="entry__activity__txt">My Little Chocobo</p>
				<time class="entry__activity__time"><span>-</span><script>ldst_strftime(1486702764, 'YMD')</script></time>
			</div>
		</div>`
	got, err := extractAchievementTimestamp(html)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1486702764, 0).UTC(); !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestStripTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text", "Hello World", "Hello World"},
		{"single tag", `<a href="x">Name</a>`, "Name"},
		{"nested tags", `<div><b>Bold</b> text</div>`, "Bold text"},
		{"html entities", `It&#39;s &amp; &lt;test&gt;`, "It's & <test>"},
		{"mixed content", `<a href="x">Name</a> on <i>World</i>`, "Name on World"},
		{"whitespace collapse", "  lots   of   space  ", "lots of space"},
		{"nbsp entity", `hello&nbsp;world`, "hello world"},
		{"br tag becomes space", `Hyur<br />Highlander / ♂`, "Hyur Highlander / ♂"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripTags(tt.in)
			if got != tt.want {
				t.Errorf("stripTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractTextBetween_StripsTags(t *testing.T) {
	html := `<p class="frame__chara__name"><a href="/character/123">Tataru Taru</a></p>`
	got := extractTextBetween(html, `class="frame__chara__name"`, "</p>")
	want := "Tataru Taru"
	if got != want {
		t.Errorf("extractTextBetween = %q, want %q", got, want)
	}
}

func TestExtractAllTextBetween_StripsTags(t *testing.T) {
	html := `<p class="character-block__name"><a href="x">Hyur</a></p><p class="character-block__name"><i>Midlander</i></p>`
	got := extractAllTextBetween(html, `class="character-block__name"`, "</p>")
	want := []string{"Hyur", "Midlander"}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("result[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestParseCharacterProfile_RealLodestoneHTML tests with actual Lodestone HTML
// patterns that caused contamination (i tags in world, br tags in race/tribe,
// nested tags in FC name).
func TestParseCharacterProfile_RealLodestoneHTML(t *testing.T) {
	// Simulate actual Lodestone HTML structure that caused contamination
	html := `
	<p class="frame__chara__name"><a href="/lodestone/character/12345">Tataru Taru</a></p>
	<p class="frame__chara__world"><i class="xiv-lds xiv-lds-home-world js__tooltip" data-tooltip="Home World"></i>Ultros [Primal]</p>
	<div class="character__profile__state"><p>Adventurer</p></div>
	<p class="character-block__name">Miqo&#39;te<br />Keeper of the Moon / ♀</p>
	<p class="character-block__name">Nophica, the Matron</p>
	<p class="character-block__name">Gridania</p>
	<p class="character-block__name">Immortal Flames / Flame Captain</p>
	<div class="character__freecompany__name"><p>Free Company</p><h4><a href="/lodestone/freecompany/1234567890/">My Free Company</a></h4></div>
	`
	profile, err := parseCharacterProfile(html, 12345)
	if err != nil {
		t.Fatalf("parseCharacterProfile: %v", err)
	}

	// Name should be clean text
	if profile.Name != "Tataru Taru" {
		t.Errorf("Name = %q, want %q", profile.Name, "Tataru Taru")
	}

	// World should NOT contain <i> tags - this was the main contamination
	if profile.World != "Ultros" {
		t.Errorf("World = %q, want %q", profile.World, "Ultros")
	}
	if profile.Datacenter != "Primal" {
		t.Errorf("Datacenter = %q, want %q", profile.Datacenter, "Primal")
	}

	// Race should be just the race name
	if profile.Race != "Miqo'te" {
		t.Errorf("Race = %q, want %q", profile.Race, "Miqo'te")
	}

	// Tribe should be parsed from first character-block__name, NOT the patron deity
	if profile.Tribe != "Keeper of the Moon" {
		t.Errorf("Tribe = %q, want %q (patron deity 'Nophica, the Matron' must NOT be stored as tribe)", profile.Tribe, "Keeper of the Moon")
	}

	// Gender should be extracted from ♀ symbol
	if profile.Gender != 2 {
		t.Errorf("Gender = %d, want %d", profile.Gender, 2)
	}

	// Grand Company should be just the company name, not rank
	if profile.GrandCompany != "Immortal Flames" {
		t.Errorf("GrandCompany = %q, want %q", profile.GrandCompany, "Immortal Flames")
	}

	// FC Name should NOT contain "Free Company" prefix or HTML tags
	if profile.FreeCompanyName != "My Free Company" {
		t.Errorf("FreeCompanyName = %q, want %q", profile.FreeCompanyName, "My Free Company")
	}

	// FC ID should be extracted from href
	if profile.FreeCompanyID != "1234567890" {
		t.Errorf("FreeCompanyID = %q, want %q", profile.FreeCompanyID, "1234567890")
	}
}

// TestParseCharacterProfile_AuRa tests the two-word "Au Ra" race name parsing.
func TestParseCharacterProfile_AuRa(t *testing.T) {
	html := `
	<p class="frame__chara__name"><a href="/lodestone/character/203">Test Au Ra</a></p>
	<p class="frame__chara__world"><i class="xiv-lds"></i>Tonberry [Elemental]</p>
	<p class="character-block__name">Au Ra<br />Xaela / ♀</p>
	<p class="character-block__name">Nald'thal, the Traders</p>
	<p class="character-block__name">Gridania</p>
	<p class="character-block__name">Maelstrom / Second Storm Lieutenant</p>
	`
	profile, err := parseCharacterProfile(html, 203)
	if err != nil {
		t.Fatalf("parseCharacterProfile: %v", err)
	}
	if profile.Race != "Au Ra" {
		t.Errorf("Race = %q, want %q", profile.Race, "Au Ra")
	}
	if profile.Tribe != "Xaela" {
		t.Errorf("Tribe = %q, want %q", profile.Tribe, "Xaela")
	}
	if profile.Gender != 2 {
		t.Errorf("Gender = %d, want %d", profile.Gender, 2)
	}
}

// TestParseCharacterProfile_MaleGender tests male gender parsing from Lodestone HTML.
func TestParseCharacterProfile_MaleGender(t *testing.T) {
	html := `
	<p class="frame__chara__name"><a href="/lodestone/character/999">Test Char</a></p>
	<p class="frame__chara__world"><i class="xiv-lds"></i>Hyperion [Primal]</p>
	<p class="character-block__name">Hyur<br />Highlander / ♂</p>
	<p class="character-block__name">Halone, the Fury</p>
	`
	profile, err := parseCharacterProfile(html, 999)
	if err != nil {
		t.Fatalf("parseCharacterProfile: %v", err)
	}
	if profile.Race != "Hyur" {
		t.Errorf("Race = %q, want %q", profile.Race, "Hyur")
	}
	if profile.Tribe != "Highlander" {
		t.Errorf("Tribe = %q, want %q", profile.Tribe, "Highlander")
	}
	if profile.Gender != 1 {
		t.Errorf("Gender = %d, want %d", profile.Gender, 1)
	}
}

// TestStripTags_LodestoneEntities tests HTML entities found in actual Lodestone pages.
func TestStripTags_LodestoneEntities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"apostrophe entity", `Miqo&#39;te`, "Miqo'te"},
		{"br tag", `Keeper of the Moon / ♀`, "Keeper of the Moon / ♀"},
		{"i tag with class", `<i class="xiv-lds xiv-lds-home-world"></i>Ultros`, "Ultros"},
		{"a tag in fc name", `<a href="/lodestone/freecompany/123/">My FC</a>`, "My FC"},
		{"nested p h4 a", `<p>Free Company</p><h4><a href="/x/">Name</a></h4>`, "Free Company Name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripTags(tt.in)
			if got != tt.want {
				t.Errorf("stripTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseClassJobs_SetsClassJobID(t *testing.T) {
	// Real Lodestone HTML: <li><img ... data-tooltip="JobName">Level</li>
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Paladin">90</li>
		<li><img src="x" data-tooltip="Warrior">80</li>
		<li><img src="x" data-tooltip="White Mage">100</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != 3 {
		t.Fatalf("parseClassJobs returned %d jobs, want 3", len(jobs))
	}

	tests := []struct {
		name       string
		classJobID uint8
		level      uint8
	}{
		{"Paladin", 19, 90},
		{"Warrior", 21, 80},
		{"White Mage", 24, 100},
	}

	for i, tt := range tests {
		if jobs[i].CharacterID != 12345 {
			t.Errorf("job[%d].CharacterID = %d, want 12345", i, jobs[i].CharacterID)
		}
		if jobs[i].ClassJobID != tt.classJobID {
			t.Errorf("job[%d].ClassJobID = %d, want %d (%s)", i, jobs[i].ClassJobID, tt.classJobID, tt.name)
		}
		if jobs[i].Name != tt.name {
			t.Errorf("job[%d].Name = %q, want %q", i, jobs[i].Name, tt.name)
		}
		if jobs[i].Level != tt.level {
			t.Errorf("job[%d].Level = %d, want %d", i, jobs[i].Level, tt.level)
		}
	}
}

func TestParseClassJobs_CombinedNames(t *testing.T) {
	// Lodestone shows "Paladin / Gladiator" for classes with job upgrades.
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Paladin / Gladiator">100</li>
		<li><img src="x" data-tooltip="Bard / Archer">50</li>
		<li><img src="x" data-tooltip="White Mage / Conjurer">80</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != 3 {
		t.Fatalf("parseClassJobs returned %d jobs, want 3", len(jobs))
	}
	// Should use first part of combined name for lookup.
	if jobs[0].Name != "Paladin" || jobs[0].ClassJobID != 19 {
		t.Errorf("job[0] = {Name:%q, ClassJobID:%d}, want {Paladin, 19}", jobs[0].Name, jobs[0].ClassJobID)
	}
	if jobs[1].Name != "Bard" || jobs[1].ClassJobID != 23 {
		t.Errorf("job[1] = {Name:%q, ClassJobID:%d}, want {Bard, 23}", jobs[1].Name, jobs[1].ClassJobID)
	}
	if jobs[2].Name != "White Mage" || jobs[2].ClassJobID != 24 {
		t.Errorf("job[2] = {Name:%q, ClassJobID:%d}, want {White Mage, 24}", jobs[2].Name, jobs[2].ClassJobID)
	}
}

func TestParseClassJobs_LimitedJobs(t *testing.T) {
	// Blue Mage and Beastmaster have "(Limited Job)" suffix.
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Blue Mage (Limited Job)">80</li>
		<li><img src="x" data-tooltip="Beastmaster (Limited Job)">-</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != 1 {
		t.Fatalf("parseClassJobs returned %d jobs, want 1 (Beastmaster has level -)", len(jobs))
	}
	if jobs[0].Name != "Blue Mage" || jobs[0].ClassJobID != 36 {
		t.Errorf("job[0] = {Name:%q, ClassJobID:%d}, want {Blue Mage, 36}", jobs[0].Name, jobs[0].ClassJobID)
	}
}

func TestParseClassJobs_SkipsUnleveled(t *testing.T) {
	// Jobs with level "-" should be skipped.
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Paladin">90</li>
		<li><img src="x" data-tooltip="Warrior">-</li>
		<li><img src="x" data-tooltip="Dark Knight">80</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != 2 {
		t.Fatalf("parseClassJobs returned %d jobs, want 2 (unleveled should be skipped)", len(jobs))
	}
	if jobs[0].Name != "Paladin" || jobs[0].Level != 90 {
		t.Errorf("job[0] = {Name:%q, Level:%d}, want {Paladin, 90}", jobs[0].Name, jobs[0].Level)
	}
	if jobs[1].Name != "Dark Knight" || jobs[1].Level != 80 {
		t.Errorf("job[1] = {Name:%q, Level:%d}, want {Dark Knight, 80}", jobs[1].Name, jobs[1].Level)
	}
}

func TestParseClassJobs_SkipsUnknownJobName(t *testing.T) {
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Paladin">90</li>
		<li><img src="x" data-tooltip="FutureJobNotInMap">50</li>
		<li><img src="x" data-tooltip="Warrior">80</li>
	</ul></div>`

	jobs := parseClassJobs(html, 99999)
	if len(jobs) != 2 {
		t.Fatalf("parseClassJobs returned %d jobs, want 2 (unknown name should be skipped)", len(jobs))
	}
	if jobs[0].Name != "Paladin" || jobs[0].ClassJobID != 19 {
		t.Errorf("job[0] = {Name:%q, ClassJobID:%d}, want {Paladin, 19}", jobs[0].Name, jobs[0].ClassJobID)
	}
	if jobs[1].Name != "Warrior" || jobs[1].ClassJobID != 21 {
		t.Errorf("job[1] = {Name:%q, ClassJobID:%d}, want {Warrior, 21}", jobs[1].Name, jobs[1].ClassJobID)
	}
}

func TestParseClassJobs_NoClassJobIDZero(t *testing.T) {
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Paladin">90</li>
		<li><img src="x" data-tooltip="Dark Knight">80</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	for _, j := range jobs {
		if j.ClassJobID == 0 {
			t.Errorf("job %q has ClassJobID=0 — this was the original bug", j.Name)
		}
	}
}

func TestParseClassJobs_AllKnownJobs(t *testing.T) {
	var entries string
	for name := range lodestoneJobIDs {
		entries += `<li><img src="x" data-tooltip="` + name + `">50</li>`
	}
	html := `<div class="character__level__list"><ul>` + entries + `</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != len(lodestoneJobIDs) {
		t.Fatalf("parseClassJobs returned %d jobs, want %d (all known jobs)", len(jobs), len(lodestoneJobIDs))
	}

	for _, j := range jobs {
		expectedID, ok := lodestoneJobIDs[j.Name]
		if !ok {
			t.Errorf("unexpected job name %q returned", j.Name)
			continue
		}
		if j.ClassJobID != expectedID {
			t.Errorf("job %q ClassJobID = %d, want %d", j.Name, j.ClassJobID, expectedID)
		}
	}
}

func TestParseClassJobs_EmptyList(t *testing.T) {
	jobs := parseClassJobs(`<div class="character__level__list"><ul></ul></div>`, 12345)
	if len(jobs) != 0 {
		t.Errorf("parseClassJobs returned %d jobs for empty list, want 0", len(jobs))
	}
}

func TestParseClassJobs_NoListSection(t *testing.T) {
	jobs := parseClassJobs(`<div>no level list here</div>`, 12345)
	if len(jobs) != 0 {
		t.Errorf("parseClassJobs returned %d jobs for missing section, want 0", len(jobs))
	}
}

func TestParseClassJobs_VariousCharacterProfiles(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		wantJobs int
	}{
		{
			name: "max_level_omni",
			html: `<div class="character__level__list"><ul>
				<li><img src="x" data-tooltip="Paladin">100</li>
				<li><img src="x" data-tooltip="Warrior">100</li>
				<li><img src="x" data-tooltip="Dark Knight">100</li>
				<li><img src="x" data-tooltip="Gunbreaker">100</li>
				<li><img src="x" data-tooltip="White Mage">100</li>
				<li><img src="x" data-tooltip="Scholar">100</li>
				<li><img src="x" data-tooltip="Astrologian">100</li>
				<li><img src="x" data-tooltip="Sage">100</li>
				<li><img src="x" data-tooltip="Monk">100</li>
				<li><img src="x" data-tooltip="Dragoon">100</li>
				<li><img src="x" data-tooltip="Ninja">100</li>
				<li><img src="x" data-tooltip="Samurai">100</li>
				<li><img src="x" data-tooltip="Reaper">100</li>
				<li><img src="x" data-tooltip="Viper">100</li>
				<li><img src="x" data-tooltip="Bard">100</li>
				<li><img src="x" data-tooltip="Machinist">100</li>
				<li><img src="x" data-tooltip="Dancer">100</li>
				<li><img src="x" data-tooltip="Black Mage">100</li>
				<li><img src="x" data-tooltip="Summoner">100</li>
				<li><img src="x" data-tooltip="Red Mage">100</li>
				<li><img src="x" data-tooltip="Pictomancer">100</li>
				<li><img src="x" data-tooltip="Blue Mage (Limited Job)">80</li>
				<li><img src="x" data-tooltip="Carpenter">100</li>
				<li><img src="x" data-tooltip="Blacksmith">100</li>
				<li><img src="x" data-tooltip="Armorer">100</li>
				<li><img src="x" data-tooltip="Goldsmith">100</li>
				<li><img src="x" data-tooltip="Leatherworker">100</li>
				<li><img src="x" data-tooltip="Weaver">100</li>
				<li><img src="x" data-tooltip="Alchemist">100</li>
				<li><img src="x" data-tooltip="Culinarian">100</li>
				<li><img src="x" data-tooltip="Miner">100</li>
				<li><img src="x" data-tooltip="Botanist">100</li>
				<li><img src="x" data-tooltip="Fisher">100</li>
			</ul></div>`,
			wantJobs: 33,
		},
		{
			name: "casual_few_jobs",
			html: `<div class="character__level__list"><ul>
				<li><img src="x" data-tooltip="Paladin">50</li>
				<li><img src="x" data-tooltip="White Mage">30</li>
				<li><img src="x" data-tooltip="Miner">15</li>
			</ul></div>`,
			wantJobs: 3,
		},
		{
			name:     "new_character_no_jobs",
			html:     `<div class="character__level__list"><ul></ul></div>`,
			wantJobs: 0,
		},
		{
			name:     "missing_level_list_section",
			html:     `<div>no job data here</div>`,
			wantJobs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := parseClassJobs(tt.html, 12345)
			if len(jobs) != tt.wantJobs {
				t.Fatalf("got %d jobs, want %d", len(jobs), tt.wantJobs)
			}
			for _, j := range jobs {
				if j.ClassJobID == 0 {
					t.Errorf("job %q has ClassJobID=0", j.Name)
				}
				if j.CharacterID != 12345 {
					t.Errorf("job %q has CharacterID=%d, want 12345", j.Name, j.CharacterID)
				}
			}
		})
	}
}

func TestNewCustomClient_WithProxyProtocolsConstruct(t *testing.T) {
	cfg := &config.LodestoneConfig{}
	for _, addr := range []string{
		"http://127.0.0.1:8080",
		"https://127.0.0.1:8443",
		"socks4://127.0.0.1:1080",
		"socks5://127.0.0.1:1080",
	} {
		c, err := NewCustomClient(cfg, nil, nil, WithProxy(addr))
		if err != nil {
			t.Fatalf("NewCustomClient with proxy %s: %v", addr, err)
		}
		if c == nil {
			t.Fatalf("NewCustomClient with proxy %s: nil client", addr)
		}
	}
	if _, err := NewCustomClient(cfg, nil, nil, WithProxy("ftp://127.0.0.1:21")); err == nil {
		t.Fatal("expected error for unsupported proxy protocol")
	}
}

func TestDoRequest_ProxyDialFailureIsTypedCheckProxy(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op:  "Get",
			URL: "https://na.finalfantasyxiv.com/lodestone/",
			Err: &httpclient.ProxyDialError{Reason: "dial", Err: errors.New("connection refused")},
		}
	}))
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckProxy {
		t.Fatalf("expected typed CheckProxy error, got %v", err)
	}
}

func TestDoRequest_429IsTypedCheckTargetWithRetryAfter(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Retry-After", "90")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("slow down")),
			Header:     header,
		}, nil
	}))
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget {
		t.Fatalf("expected typed CheckTarget error, got %v", err)
	}
	if checkErr.RetryAfter != 90*time.Second {
		t.Fatalf("expected Retry-After 90s, got %v", checkErr.RetryAfter)
	}
	var dialErr *httpclient.ProxyDialError
	if errors.As(err, &dialErr) {
		t.Fatal("a target 429 must not be classified as a proxy dial failure")
	}
}

func TestDoRequest_ProxyDialFailureAbortsLadder(t *testing.T) {
	attempts := 0
	c := &CustomClient{
		httpClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, &url.Error{
				Op:  "Get",
				URL: "https://na.finalfantasyxiv.com/lodestone/character/1/",
				Err: &httpclient.ProxyDialError{Reason: "dial", Err: errors.New("connection refused")},
			}
		})},
		limiter:     rate.NewLimiter(rate.Inf, 1),
		logger:      loggerOrDiscard(nil),
		maxRetries:  3,
		backoffBase: time.Millisecond,
	}

	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckProxy {
		t.Fatalf("expected typed CheckProxy error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("a conclusive proxy dial failure must abort the ladder after 1 attempt, got %d", attempts)
	}
}

func TestNewCustomClient_RateLimitClampedAndFallback(t *testing.T) {
	tests := []struct {
		name      string
		rateLimit float64
		want      float64
	}{
		{name: "configured rate passes through", rateLimit: 0.1, want: 0.1},
		{name: "ceiling clamps to one request per second", rateLimit: 5.0, want: 1.0},
		{name: "zero falls back to the safe ceiling", rateLimit: 0, want: 1.0},
		{name: "negative falls back to the safe ceiling", rateLimit: -3, want: 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewCustomClient(&config.LodestoneConfig{RateLimit: tt.rateLimit}, nil, nil)
			if err != nil {
				t.Fatalf("NewCustomClient: %v", err)
			}
			if got := c.limiter.Limit(); got != rate.Limit(tt.want) {
				t.Fatalf("limiter rate = %v, want %v", got, tt.want)
			}
			if c.limiter.Burst() != 1 {
				t.Fatalf("limiter burst = %d, want 1 (strict spacing)", c.limiter.Burst())
			}
		})
	}
}

func TestDoRequest_202ChallengeIsTypedCheckTargetWithoutRetry(t *testing.T) {
	attempts := 0
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("challenge interstitial")),
		}, nil
	}))
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget {
		t.Fatalf("expected typed CheckTarget error for a 202 challenge, got %v", err)
	}
	// A challenge will not clear inside the backoff ladder: fail immediately
	// so the worker cools this proxy's destination down and the queue
	// redelivers on a different identity.
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", attempts)
	}
	var dialErr *httpclient.ProxyDialError
	if errors.As(err, &dialErr) {
		t.Fatal("a target 202 must not be classified as a proxy dial failure")
	}
}

func TestDoRequest_403CloudflareChallengeIsTypedCheckTargetWithoutRetry(t *testing.T) {
	attempts := 0
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Cf-Mitigated": []string{"challenge"}},
			Body:       io.NopCloser(strings.NewReader("<html>Just a moment...</html>")),
		}, nil
	}))
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget {
		t.Fatalf("expected typed CheckTarget error for a Cloudflare 403 challenge, got %v", err)
	}
	if !checkErr.Challenge {
		t.Fatal("the typed error must flag the challenge for diagnostics")
	}
	if attempts != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", attempts)
	}
}

func TestDoRequest_403ChallengeBodyMarkerIsTypedCheckTarget(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("<html><title>Attention Required! | Cloudflare</title></html>")),
		}, nil
	}))
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget {
		t.Fatalf("expected typed CheckTarget error for a challenge-body 403, got %v", err)
	}
}

func TestDoRequest_Genuine403PassesThroughToCaller(t *testing.T) {
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("<html>Lodestone error: access restricted.</html>")),
		}, nil
	}))
	body, status, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	if err != nil {
		t.Fatalf("expected genuine 403 to pass through, got %v", err)
	}
	if status != http.StatusForbidden || body == nil {
		t.Fatalf("expected status 403 with body, got status %d body %v", status, body)
	}
	var checkErr *contract.ProxyCheckError
	if errors.As(err, &checkErr) {
		t.Fatal("a genuine Lodestone 403 must not be typed as a proxy check error")
	}
}

func testCustomClientWithLimiter(rt http.RoundTripper, rl contract.ProviderRateLimiter, maxRetries int) *CustomClient {
	return &CustomClient{
		httpClient:  &http.Client{Transport: rt},
		limiter:     rate.NewLimiter(rate.Inf, 1),
		logger:      loggerOrDiscard(nil),
		rateLimiter: rl,
		maxRetries:  maxRetries,
		backoffBase: time.Millisecond,
	}
}

func TestCustomClient_FetchAchievements_404IsCharacterNotFound(t *testing.T) {
	requests := 0
	c := testCustomClient(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("<html>Not Found</html>")),
			Header:     make(http.Header),
		}, nil
	}))
	_, err := c.FetchAchievements(context.Background(), 1, nil)
	if !errors.Is(err, contract.ErrCharacterNotFound) {
		t.Fatalf("a genuine Lodestone 404 must surface as contract.ErrCharacterNotFound, got %v", err)
	}
	if requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1", requests)
	}
}

func TestDoRequest_429PausesAndReturnsImmediately(t *testing.T) {
	requests := 0
	rl := mock.NewProviderRateLimiter()
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests++
		header := make(http.Header)
		header.Set("Retry-After", "90")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader("slow down")),
			Header:     header,
		}, nil
	}), rl, 3)
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckTarget {
		t.Fatalf("expected typed CheckTarget error, got %v", err)
	}
	// The destination needs quiet: no in-client retry ladder on a 429.
	if requests != 1 {
		t.Fatalf("HTTP requests = %d, want 1 (429 must return immediately)", requests)
	}
	if rl.IsAvailable(contract.ProviderLodestone) {
		t.Fatal("lodestone provider must be paused after a 429")
	}
	until, ok := rl.PausedUntil(contract.ProviderLodestone)
	if !ok {
		t.Fatal("expected paused-until to be set after a 429")
	}
	if remain := time.Until(until); remain < 80*time.Second {
		t.Fatalf("pause must honor the 90s Retry-After, got %v remaining", remain)
	}
}

func TestDoRequest_202ChallengePausesLimiter(t *testing.T) {
	rl := mock.NewProviderRateLimiter()
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       io.NopCloser(strings.NewReader("challenge interstitial")),
		}, nil
	}), rl, 3)
	_, _, _ = c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	if rl.IsAvailable(contract.ProviderLodestone) {
		t.Fatal("lodestone provider must be paused after a 202 challenge")
	}
}

func TestDoRequest_403ChallengePausesLimiter(t *testing.T) {
	rl := mock.NewProviderRateLimiter()
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Cf-Mitigated": []string{"challenge"}},
			Body:       io.NopCloser(strings.NewReader("<html>Just a moment...</html>")),
		}, nil
	}), rl, 3)
	_, _, _ = c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	if rl.IsAvailable(contract.ProviderLodestone) {
		t.Fatal("lodestone provider must be paused after a 403 challenge")
	}
}

func TestDoRequest_WaitsForPausedProviderBeforeRequest(t *testing.T) {
	rl := mock.NewProviderRateLimiter()
	rl.Pause(contract.ProviderLodestone, 150*time.Millisecond, "test pause")
	requestedAt := time.Time{}
	c := testCustomClientWithLimiter(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		requestedAt = time.Now()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("<html>ok</html>")),
			Header:     make(http.Header),
		}, nil
	}), rl, 0)
	_, _, err := c.doRequest(context.Background(), "https://na.finalfantasyxiv.com/lodestone/character/1/")
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	if requestedAt.IsZero() {
		t.Fatal("expected a request to be made")
	}
	until, _ := rl.PausedUntil(contract.ProviderLodestone)
	if requestedAt.Before(until.Add(-50 * time.Millisecond)) {
		t.Fatalf("request must wait out the provider pause: requested at %v, pause until %v", requestedAt, until)
	}
}

// TestCustomClient_CloseIdleConnections proves the client closes the idle
// connection its transport keeps alive, which is what releases a replaced
// proxy's tunnel.
func TestCustomClient_CloseIdleConnections(t *testing.T) {
	closed := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	srv.Start()
	defer srv.Close()

	c := &CustomClient{httpClient: &http.Client{Transport: httpclient.NewDirectTransport()}, limiter: rate.NewLimiter(rate.Inf, 1), logger: loggerOrDiscard(nil)}
	resp, err := c.httpClient.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	select {
	case <-closed:
		t.Fatal("connection closed before CloseIdleConnections; keep-alive not exercised")
	case <-time.After(50 * time.Millisecond):
	}
	c.CloseIdleConnections()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection still open after CloseIdleConnections")
	}
}

// TestParseClassJobs_RogueAndLevelledBeastmaster keeps the two entries the
// job table once missed: Rogue (29, Ninja's base class) and Beastmaster
// (43, the limited job added in patch 7.56).
func TestParseClassJobs_RogueAndLevelledBeastmaster(t *testing.T) {
	html := `<div class="character__level__list"><ul>
		<li><img src="x" data-tooltip="Rogue">27</li>
		<li><img src="x" data-tooltip="Beastmaster (Limited Job)">50</li>
	</ul></div>`

	jobs := parseClassJobs(html, 12345)
	if len(jobs) != 2 {
		t.Fatalf("parseClassJobs returned %d jobs, want 2: %+v", len(jobs), jobs)
	}
	if jobs[0].Name != "Rogue" || jobs[0].ClassJobID != 29 || jobs[0].Level != 27 {
		t.Errorf("job[0] = %+v, want Rogue 29 at 27", jobs[0])
	}
	if jobs[1].Name != "Beastmaster" || jobs[1].ClassJobID != 43 || jobs[1].Level != 50 {
		t.Errorf("job[1] = %+v, want Beastmaster 43 at 50", jobs[1])
	}
}
