// Package lodestone provides a custom HTTP client for scraping The Lodestone.
// This replaces the godestone-based client with direct HTML scraping using
// the achievement detail endpoint for sequential milestone checking.
package lodestone

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
	"golang.org/x/time/rate"

	"github.com/mihaiflorentin88/ffxiv-census/config"
	httpclient "github.com/mihaiflorentin88/ffxiv-census/infrastructure/httpclient"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const (
	userAgent      = "Mozilla/5.0 (compatible; FFXIV-Census-Engine/1.0)"
	requestTimeout = 30 * time.Second
	rateLimitPause = 30 * time.Second
	challengePause = 5 * time.Second
	maxSafeRate    = 1.0
)

func loggerOrDiscard(l contract.Logger) contract.Logger {
	if l == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return l
}

func jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 0.9 + 0.2*rand.Float64()
	return time.Duration(float64(d) * factor)
}

// isTimeoutError reports whether the transport error is a deadline/timeout
// (client timeout, dial timeout, context deadline) rather than an instant
// failure such as a connection reset.
func isTimeoutError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// stripTags removes HTML tags, decodes common entities, and collapses whitespace.
var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	entityMap = map[string]string{
		"&#39;":  "'",
		"&amp;":  "&",
		"&lt;":   "<",
		"&gt;":   ">",
		"&quot;": `"`,
		"&#34;":  `"`,
		"&nbsp;": " ",
	}
	multiSpaceRe = regexp.MustCompile(`\s+`)
)

// lodestoneJobIDs maps English job/class names (as rendered on The Lodestone
// character profile page) to their official FFXIV ClassJobID values.
// These IDs are stable across patches — Square Enix never reassigns existing IDs.
// New jobs (e.g. from expansions) need entries added here.
var lodestoneJobIDs = map[string]uint8{
	// Tanks
	"Paladin":     19,
	"Warrior":     21,
	"Dark Knight": 32,
	"Gunbreaker":  37,
	// Healers
	"White Mage":  24,
	"Scholar":     28,
	"Astrologian": 33,
	"Sage":        40,
	// Melee DPS
	"Monk":    20,
	"Dragoon": 22,
	"Ninja":   30,
	"Samurai": 34,
	"Reaper":  39,
	"Viper":   41,
	// Physical Ranged DPS
	"Bard":      23,
	"Machinist": 31,
	"Dancer":    38,
	// Magic Ranged DPS
	"Black Mage":  25,
	"Summoner":    27,
	"Red Mage":    35,
	"Pictomancer": 42,
	"Blue Mage":   36,
	// Disciples of the Hand (Crafters)
	"Carpenter":     8,
	"Blacksmith":    9,
	"Armorer":       10,
	"Goldsmith":     11,
	"Leatherworker": 12,
	"Weaver":        13,
	"Alchemist":     14,
	"Culinarian":    15,
	// Disciples of the Land (Gatherers)
	"Miner":    16,
	"Botanist": 17,
	"Fisher":   18,
	// Base Classes (still appear on Lodestone for some characters)
	"Gladiator":   1,
	"Pugilist":    2,
	"Marauder":    3,
	"Lancer":      4,
	"Archer":      5,
	"Conjurer":    6,
	"Thaumaturge": 7,
	"Arcanist":    26,
	"Rogue":       29,
}

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	for entity, replacement := range entityMap {
		s = strings.ReplaceAll(s, entity, replacement)
	}
	s = multiSpaceRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// CustomClientOption configures the custom Lodestone client.
type CustomClientOption func(*CustomClient)

// WithProxy routes all HTTP requests through the given proxy URL.
// Supports http, https, socks4, and socks5 protocols.
func WithProxy(proxyURL string) CustomClientOption {
	return func(c *CustomClient) {
		c.proxyURL = proxyURL
	}
}

// CustomClient is a contract.LodestoneClient that scrapes The Lodestone
// directly via HTTP, using the achievement detail endpoint for milestone checks.
type CustomClient struct {
	httpClient  *http.Client
	limiter     *rate.Limiter
	maxRetries  int
	backoffBase time.Duration
	logger      contract.Logger
	rateLimiter contract.ProviderRateLimiter
	proxyURL    string
}

// NewCustomClient builds a custom Lodestone HTTP client.
func NewCustomClient(cfg *config.LodestoneConfig, logger contract.Logger, rateLimiter contract.ProviderRateLimiter, opts ...CustomClientOption) (*CustomClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("lodestone config is nil")
	}

	rps := cfg.RateLimit
	if rps <= 0 {
		rps = maxSafeRate
	}
	if rps > maxSafeRate {
		rps = maxSafeRate
	}

	c := &CustomClient{
		httpClient:  &http.Client{Timeout: requestTimeout, Transport: httpclient.NewDirectTransport()},
		limiter:     rate.NewLimiter(rate.Limit(rps), 1),
		maxRetries:  cfg.MaxRetries,
		backoffBase: 500 * time.Millisecond,
		logger:      loggerOrDiscard(logger),
		rateLimiter: rateLimiter,
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.proxyURL != "" {
		transport, err := newProxyTransport(c.proxyURL)
		if err != nil {
			return nil, fmt.Errorf("lodestone proxy transport: %w", err)
		}
		c.httpClient.Transport = transport
	}

	return c, nil
}

// CloseIdleConnections closes the idle keep-alive connections of the transport
// this client owns. Workers call it when they swap the client's proxy or exit,
// so the tunnel to the old proxy is not left open until its idle timeout.
func (c *CustomClient) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

// newProxyTransport creates an HTTP transport that routes through the given
// proxy via the shared cancellation-safe transport builder.
func newProxyTransport(proxyURL string) (*http.Transport, error) {
	return httpclient.NewProxyTransport(proxyURL, requestTimeout)
}

// isCloudflareChallenge reports whether the response is a Cloudflare block
// rather than a genuine Lodestone response. Cloudflare flags active
// challenges with the Cf-Mitigated header and serves HTML interstitials or
// block pages whose markers never appear on Lodestone error pages.
func isCloudflareChallenge(resp *http.Response, body []byte) bool {
	if strings.EqualFold(resp.Header.Get("Cf-Mitigated"), "challenge") {
		return true
	}
	if len(body) == 0 {
		return false
	}
	sniff := body
	if len(sniff) > 64*1024 {
		sniff = sniff[:64*1024]
	}
	lower := strings.ToLower(string(sniff))
	for _, marker := range []string{
		"just a moment",
		"attention required",
		"cf-error-details",
		"error 1020",
		"cloudflare",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// doRequest executes an HTTP GET with rate limiting, retries, and backoff.
func (c *CustomClient) doRequest(ctx context.Context, url string) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		// The provider-wide limiter (e.g. a Lodestone 429 cooldown) gates
		// every attempt, including the first: without this wait a paused
		// provider keeps getting hit at full speed.
		if c.rateLimiter != nil {
			if err := c.rateLimiter.WaitUntilAvailable(ctx, contract.ProviderLodestone); err != nil {
				return nil, 0, err
			}
		}

		if err := c.limiter.Wait(ctx); err != nil {
			return nil, 0, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Proven proxy dial/negotiation failures are re-attributed as a
			// typed conclusive error; ambiguous failures stay unclassified.
			lastErr = httpclient.WrapProxyDial(err)
			// A conclusive proxy death cannot recover by retrying through the
			// same proxy: return immediately so the worker rotates identities
			// instead of burning attempts and rate tokens.
			var dialCheck *contract.ProxyCheckError
			if errors.As(lastErr, &dialCheck) && dialCheck.Kind == contract.CheckProxy {
				c.logger.WarnContext(ctx, "lodestone.proxy_failure",
					slog.String("url", url),
					slog.String("error", err.Error()))
				return nil, 0, fmt.Errorf("request %s: %w", url, lastErr)
			}
			// A full response timeout through an established path will not
			// clear by retrying the same proxy after a sub-second backoff:
			// return immediately so the queue's retry ladder redelivers —
			// usually through a different proxy — instead of the goroutine
			// babysitting the request for minutes.
			if isTimeoutError(err) {
				c.logger.WarnContext(ctx, "lodestone.request_timeout",
					slog.String("url", url),
					slog.Int("attempt", attempt+1),
					slog.String("error", err.Error()))
				return nil, 0, fmt.Errorf("request %s: %w", url, lastErr)
			}
			backoff := c.backoffBase * time.Duration(1<<uint(attempt))
			c.logger.WarnContext(ctx, "lodestone.request_retry",
				slog.String("url", url),
				slog.Int("attempt", attempt+1),
				slog.Int("max_attempts", c.maxRetries+1),
				slog.Duration("backoff", backoff),
				slog.String("error", err.Error()))
			if attempt < c.maxRetries {
				timer := time.NewTimer(jittered(backoff))
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, 0, ctx.Err()
				case <-timer.C:
				}
			}
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			if c.rateLimiter != nil {
				pause := retryAfter
				if pause <= 0 {
					pause = rateLimitPause
				}
				c.rateLimiter.Pause(contract.ProviderLodestone, pause, "lodestone 429 rate limited")
			}
			c.logger.WarnContext(ctx, "lodestone.rate_limited",
				slog.String("url", url),
				slog.Int("status", resp.StatusCode),
				slog.Duration("retry_after", retryAfter))
			// The destination itself is rate limiting this identity: surface a
			// typed target error carrying the Retry-After hint and return
			// immediately — an in-client retry cannot help while the provider
			// is paused, and the queue's retry ladder re-delivers after the
			// pause instead.
			return nil, resp.StatusCode, fmt.Errorf("request %s: %w", url, &contract.ProxyCheckError{
				Kind:       contract.CheckTarget,
				Reason:     "status 429",
				RetryAfter: retryAfter,
				Err:        fmt.Errorf("HTTP %d from %s", resp.StatusCode, url),
			})
		}

		if resp.StatusCode == http.StatusAccepted {
			c.pauseForChallenge(resp)

			lastErr = &contract.ProxyCheckError{
				Kind:       contract.CheckTarget,
				Reason:     "status 202",
				Challenge:  true,
				RetryAfter: httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
				Err:        fmt.Errorf("HTTP %d from %s", resp.StatusCode, url),
			}
			c.logger.WarnContext(ctx, "lodestone.challenge",
				slog.String("url", url),
				slog.Int("status", resp.StatusCode))
			return nil, resp.StatusCode, fmt.Errorf("request %s: %w", url, lastErr)
		}

		if resp.StatusCode == http.StatusForbidden && isCloudflareChallenge(resp, body) {
			// Cloudflare also blocks identities with 403 interstitials.
			// Treating that as the Lodestone "restricted" outcome would
			// silently drop a real character forever, so it must surface
			// as a target rejection like the 202 challenge: the worker
			// cools the destination down and the queue redelivers on
			// another identity. A genuine Lodestone 403 (restricted
			// profile, private achievements) carries none of the
			// Cloudflare markers and keeps its conclusive handling in
			// the callers.
			c.pauseForChallenge(resp)
			lastErr = &contract.ProxyCheckError{
				Kind:       contract.CheckTarget,
				Reason:     "status 403 challenge",
				Challenge:  true,
				RetryAfter: httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
				Err:        fmt.Errorf("HTTP %d from %s", resp.StatusCode, url),
			}
			c.logger.WarnContext(ctx, "lodestone.challenge",
				slog.String("url", url),
				slog.Int("status", resp.StatusCode))
			return nil, resp.StatusCode, fmt.Errorf("request %s: %w", url, lastErr)
		}

		return body, resp.StatusCode, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("request %s: all attempts failed", url)
	}
	return nil, 0, fmt.Errorf("request %s: %w", url, lastErr)
}

// pauseForChallenge applies a short provider pause after a Cloudflare
// interstitial (202 or a 403 block page): the destination is hostile to
// this identity right now. The Retry-After hint is honored when present;
// otherwise a 5s default keeps the next delivery from hammering the
// challenge page.
func (c *CustomClient) pauseForChallenge(resp *http.Response) {
	if c.rateLimiter == nil {
		return
	}
	pause := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if pause <= 0 {
		pause = challengePause
	}
	c.rateLimiter.Pause(contract.ProviderLodestone, pause, "lodestone challenge")
}

// FetchCharacter scrapes a character profile from The Lodestone.
func (c *CustomClient) FetchCharacter(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
	start := time.Now()
	charURL := fmt.Sprintf("https://na.finalfantasyxiv.com/lodestone/character/%d/", id)

	c.logger.InfoContext(ctx, "lodestone.fetch_character.attempt",
		slog.Uint64("character_id", uint64(id)),
		slog.String("proxy", c.proxyURL))

	body, statusCode, err := c.doRequest(ctx, charURL)
	if err != nil {
		c.logger.ErrorContext(ctx, "lodestone.fetch_character.error",
			slog.Uint64("character_id", uint64(id)),
			slog.Int("status", statusCode),
			slog.Any("error", err))
		return nil, fmt.Errorf("fetch character %d: %w", id, err)
	}

	if statusCode == http.StatusNotFound || statusCode == http.StatusForbidden {
		c.logger.InfoContext(ctx, "lodestone.fetch_character.not_found",
			slog.Uint64("character_id", uint64(id)),
			slog.Int("status", statusCode))
		return nil, contract.ErrCharacterNotFound
	}

	if statusCode != http.StatusOK {
		c.logger.ErrorContext(ctx, "lodestone.fetch_character.error",
			slog.Uint64("character_id", uint64(id)),
			slog.Int("status", statusCode),
			slog.Any("error", fmt.Errorf("unexpected status %d", statusCode)))
		return nil, fmt.Errorf("fetch character %d: HTTP %d", id, statusCode)
	}

	html := string(body)
	profile, err := parseCharacterProfile(html, id)
	if err != nil {
		return nil, fmt.Errorf("parse character %d: %w", id, err)
	}

	duration := time.Since(start)
	c.logger.InfoContext(ctx, "lodestone.fetch_character.success",
		slog.Uint64("character_id", uint64(id)),
		slog.Int("status", statusCode),
		slog.String("name", profile.Name),
		slog.String("world", profile.World),
		slog.Duration("duration", duration))

	return profile, nil
}

// FetchAchievements checks milestone achievements using the detail endpoint.
// Milestones are checked sequentially; if one is missing, checking stops
// (sequential dependency: later milestones require earlier ones).
func (c *CustomClient) FetchAchievements(ctx context.Context, charID uint32, milestoneIDs []uint32) (*contract.AchievementSummary, error) {
	start := time.Now()

	c.logger.DebugContext(ctx, "lodestone.fetch_achievements.start",
		slog.Uint64("character_id", uint64(charID)),
		slog.Int("milestones_to_check", len(milestoneIDs)))

	listURL := fmt.Sprintf("https://na.finalfantasyxiv.com/lodestone/character/%d/achievement/", charID)
	body, statusCode, err := c.doRequest(ctx, listURL)
	if err != nil {
		return nil, fmt.Errorf("fetch achievement list for character %d: %w", charID, err)
	}
	if statusCode == http.StatusNotFound {
		// A genuine Lodestone 404 is terminal: the character does not exist
		// (or was deleted). Callers mark the character deleted instead of
		// retrying forever.
		c.logger.InfoContext(ctx, "lodestone.fetch_achievements.not_found",
			slog.Uint64("character_id", uint64(charID)))
		return nil, contract.ErrCharacterNotFound
	}
	if statusCode == http.StatusForbidden {
		return &contract.AchievementSummary{Private: true}, nil
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("achievement list %d: HTTP %d", charID, statusCode)
	}
	latest, err := extractLatestAchievement(string(body))
	if err != nil {
		return nil, fmt.Errorf("parse latest achievement for character %d: %w", charID, err)
	}

	var results []contract.AchievementResult
	requestsMade := 1

	for _, id := range milestoneIDs {
		result, err := c.checkSingleAchievement(ctx, charID, id)
		if errors.Is(err, contract.ErrAchievementsPrivate) {
			return &contract.AchievementSummary{Private: true}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("check achievement %d for character %d: %w", id, charID, err)
		}
		requestsMade++

		c.logger.DebugContext(ctx, "lodestone.fetch_achievements.checked",
			slog.Uint64("character_id", uint64(charID)),
			slog.Uint64("achievement_id", uint64(id)),
			slog.String("achievement_name", result.Name),
			slog.Bool("earned", result.Earned),
			slog.String("earned_at", result.EarnedAt.Format(time.RFC3339)))

		if result.Earned {
			results = append(results, result)
		} else {
			// Sequential dependency — no point checking further.
			c.logger.InfoContext(ctx, "lodestone.check_achievement.stopping",
				slog.Uint64("character_id", uint64(charID)),
				slog.Uint64("missing_id", uint64(id)),
				slog.String("missing_name", result.Name),
				slog.String("reason", "sequential_dependency"),
				slog.Int("milestones_found", len(results)))
			break
		}
	}

	summary := &contract.AchievementSummary{
		Milestones:        results,
		LatestAchievement: latest,
	}

	duration := time.Since(start)
	c.logger.DebugContext(ctx, "lodestone.fetch_achievements.complete",
		slog.Uint64("character_id", uint64(charID)),
		slog.Int("milestones_found", len(results)),
		slog.Int("requests_made", requestsMade),
		slog.Duration("total_duration", duration))

	return summary, nil
}

func extractLatestAchievement(html string) (*contract.AchievementResult, error) {
	doc, err := xhtml.Parse(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse achievement list HTML: %w", err)
	}
	row := findHTMLNode(doc, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "a" && hasHTMLClass(node, "entry__achievement") && achievementDetailIDRe.MatchString(htmlNodeAttr(node, "href"))
	})
	if row == nil {
		return nil, nil
	}
	timeNode := findHTMLNode(row, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "time" && hasHTMLClass(node, "entry__activity__time")
	})
	if timeNode == nil {
		return nil, fmt.Errorf("latest completed achievement timestamp not found")
	}
	match := timestampEpochRe.FindStringSubmatch(htmlNodeText(timeNode))
	if len(match) < 2 {
		return nil, fmt.Errorf("latest completed achievement timestamp not found")
	}
	epoch, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse epoch: %w", err)
	}
	idMatch := achievementDetailIDRe.FindStringSubmatch(htmlNodeAttr(row, "href"))
	if len(idMatch) < 2 {
		return nil, fmt.Errorf("latest completed achievement ID not found")
	}
	id, err := strconv.ParseUint(idMatch[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse achievement ID: %w", err)
	}
	nameNode := findHTMLNode(row, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && hasHTMLClass(node, "entry__activity__txt")
	})
	result := &contract.AchievementResult{AchievementID: uint32(id), Earned: true, EarnedAt: time.Unix(epoch, 0).UTC()}
	if nameNode != nil {
		result.Name = achievementNameFromActivityText(htmlNodeText(nameNode))
	}
	return result, nil
}

// checkSingleAchievement checks if a character has earned a specific achievement.
func (c *CustomClient) checkSingleAchievement(ctx context.Context, charID uint32, achievementID uint32) (contract.AchievementResult, error) {
	start := time.Now()
	detailURL := fmt.Sprintf("https://na.finalfantasyxiv.com/lodestone/character/%d/achievement/detail/%d/", charID, achievementID)

	c.logger.InfoContext(ctx, "lodestone.check_achievement.attempt",
		slog.Uint64("character_id", uint64(charID)),
		slog.Uint64("achievement_id", uint64(achievementID)))

	body, statusCode, err := c.doRequest(ctx, detailURL)
	if err != nil {
		return contract.AchievementResult{}, err
	}

	duration := time.Since(start)
	html := string(body)

	result := contract.AchievementResult{
		AchievementID: achievementID,
	}

	// Extract achievement name from HTML.
	if name := extractAchievementName(html); name != "" {
		result.Name = name
	}

	if statusCode == http.StatusForbidden {
		c.logger.DebugContext(ctx, "lodestone.check_achievement.private",
			slog.Uint64("character_id", uint64(charID)),
			slog.Uint64("achievement_id", uint64(achievementID)),
			slog.String("achievement_name", result.Name),
			slog.Duration("duration", duration))
		return result, contract.ErrAchievementsPrivate
	}

	if statusCode != http.StatusOK {
		return contract.AchievementResult{}, fmt.Errorf("achievement detail %d/%d: HTTP %d", charID, achievementID, statusCode)
	}

	// Check for earned indicator.
	if strings.Contains(html, "entry__achievement__view--complete") {
		earnedAt, err := extractAchievementTimestamp(html)
		if err != nil {
			return contract.AchievementResult{}, fmt.Errorf("parse earned timestamp for achievement %d: %w", achievementID, err)
		}
		result.Earned = true
		result.EarnedAt = earnedAt.UTC()
		c.logger.InfoContext(ctx, "lodestone.check_achievement.earned",
			slog.Uint64("character_id", uint64(charID)),
			slog.Uint64("achievement_id", uint64(achievementID)),
			slog.String("achievement_name", result.Name),
			slog.Duration("duration", duration))
	} else {
		c.logger.DebugContext(ctx, "lodestone.check_achievement.not_earned",
			slog.Uint64("character_id", uint64(charID)),
			slog.Uint64("achievement_id", uint64(achievementID)),
			slog.String("achievement_name", result.Name),
			slog.Duration("duration", duration))
	}

	return result, nil
}

// HTML parsing helpers.

var (
	timestampEpochRe          = regexp.MustCompile(`ldst_strftime\((\d+),`)
	achievementDetailIDRe     = regexp.MustCompile(`/achievement/detail/(\d+)/`)
	achievementActivityNameRe = regexp.MustCompile(`(?i)character achievement\s+"([^"]+)"\s+earned`)
	nameRe                    = regexp.MustCompile(`entry__activity__txt">([^<]+)</p>`)
	worldDCRe                 = regexp.MustCompile(`^([^\s]+)\s+\[([^\]]+)\]$`)
	fcIDRe                    = regexp.MustCompile(`/lodestone/freecompany/(\d+)/`)
)

// extractAchievementTimestamp extracts the epoch only from the completed
// achievement row, never from page-global navigation timestamps.
func extractAchievementTimestamp(html string) (time.Time, error) {
	doc, err := xhtml.Parse(strings.NewReader(html))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse achievement HTML: %w", err)
	}
	completeRow := findHTMLNode(doc, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "div" && hasHTMLClass(node, "entry__achievement__view--complete")
	})
	if completeRow == nil {
		return time.Time{}, fmt.Errorf("completed achievement row not found")
	}
	timeNode := findHTMLNode(completeRow, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "time" && hasHTMLClass(node, "entry__activity__time")
	})
	if timeNode == nil {
		return time.Time{}, fmt.Errorf("completed achievement timestamp not found")
	}
	match := timestampEpochRe.FindStringSubmatch(htmlNodeText(timeNode))
	if len(match) < 2 {
		return time.Time{}, fmt.Errorf("completed achievement timestamp not found")
	}
	epoch, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse epoch: %w", err)
	}
	return time.Unix(epoch, 0).UTC(), nil
}

func findHTMLNode(root *xhtml.Node, match func(*xhtml.Node) bool) *xhtml.Node {
	if match(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findHTMLNode(child, match); found != nil {
			return found
		}
	}
	return nil
}

func hasHTMLClass(node *xhtml.Node, want string) bool {
	for _, attr := range node.Attr {
		if attr.Key == "class" {
			for _, className := range strings.Fields(attr.Val) {
				if className == want {
					return true
				}
			}
		}
	}
	return false
}

func htmlNodeText(root *xhtml.Node) string {
	var text strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return text.String()
}

func htmlNodeAttr(node *xhtml.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func achievementNameFromActivityText(text string) string {
	if match := achievementActivityNameRe.FindStringSubmatch(strings.TrimSpace(text)); len(match) == 2 {
		return match[1]
	}
	return strings.TrimSpace(text)
}

// extractAchievementName extracts the achievement name from the detail page HTML.
func extractAchievementName(html string) string {
	match := nameRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// parseCharacterProfile extracts character data from the Lodestone profile page HTML.
func parseCharacterProfile(html string, id uint32) (*contract.CharacterProfile, error) {
	profile := &contract.CharacterProfile{ID: id}

	// Name: p.frame__chara__name
	profile.Name = extractTextBetween(html, `class="frame__chara__name"`, "</p>")
	profile.Name = strings.TrimSpace(profile.Name)

	if profile.Name == "" {
		return nil, fmt.Errorf("could not parse character name from HTML")
	}

	// World + DC: p.frame__chara__world — format "World [DC]"
	worldText := extractTextBetween(html, `class="frame__chara__world"`, "</p>")
	worldText = strings.TrimSpace(worldText)
	worldText = strings.TrimPrefix(worldText, "(")
	worldText = strings.TrimSuffix(worldText, ")")
	if m := worldDCRe.FindStringSubmatch(worldText); len(m) >= 3 {
		profile.World = m[1]
		profile.Datacenter = m[2]
	} else {
		profile.World = worldText
	}

	// Bio: .character__profile__state
	profile.Bio = extractTextBetween(html, `class="character__profile__state"`, "</p>")
	profile.Bio = strings.TrimSpace(profile.Bio)

	// Race/Tribe/Gender: .character-block__name entries
	// New Lodestone HTML puts race, tribe, and gender in a single element:
	// <p class="character-block__name">Roegadyn<br />Hellsguard / ♂</p>
	// After stripTags: "Roegadyn Hellsguard / ♂"
	raceTribe := extractAllTextBetween(html, `class="character-block__name"`, "</p>")
	if len(raceTribe) >= 1 {
		combined := strings.TrimSpace(raceTribe[0])
		// Extract gender from ♂/♀ symbol
		if strings.Contains(combined, "♂") {
			profile.Gender = 1
			combined = strings.ReplaceAll(combined, "♂", "")
		} else if strings.Contains(combined, "♀") {
			profile.Gender = 2
			combined = strings.ReplaceAll(combined, "♀", "")
		}
		// Split on " / " to separate race/tribe from gender suffix
		if parts := strings.SplitN(combined, " / ", 2); len(parts) == 2 {
			combined = parts[0]
		}
		combined = strings.TrimSpace(combined)
		// Split race from tribe. "Au Ra" is the only two-word race name.
		if strings.HasPrefix(combined, "Au Ra ") {
			profile.Race = "Au Ra"
			profile.Tribe = strings.TrimSpace(combined[6:])
		} else if idx := strings.Index(combined, " "); idx > 0 {
			profile.Race = combined[:idx]
			profile.Tribe = strings.TrimSpace(combined[idx+1:])
		} else {
			profile.Race = combined
		}
	}
	if len(raceTribe) >= 2 && profile.Tribe == "" {
		profile.Tribe = strings.TrimSpace(raceTribe[1])
	}

	// Grand Company: 4th character-block__name entry (index 3)
	// Format: "Maelstrom / Second Storm Lieutenant" — take only the company name
	if len(raceTribe) >= 4 {
		gcText := strings.TrimSpace(raceTribe[3])
		if parts := strings.SplitN(gcText, " / ", 2); len(parts) >= 1 {
			profile.GrandCompany = strings.TrimSpace(parts[0])
		}
	}

	// FC Name: extract from <a> inside character__freecompany__name
	// HTML: <p>Free Company</p><h4><a href="/lodestone/freecompany/123/">FC Name</a></h4>
	// extractTextBetween with "</a>" returns "Free Company...FC Name" — use extractHref approach instead
	fcNameText := extractTextBetween(html, `class="character__freecompany__name"`, "</a>")
	// Strip the "Free Company" prefix that appears in the <p> tag before the <a>
	fcNameText = strings.TrimPrefix(fcNameText, "Free Company")
	fcNameText = strings.TrimSpace(fcNameText)
	if fcNameText == "" {
		fcNameText = extractTextBetween(html, `class="character__freecompany__name"`, "</h4>")
		fcNameText = strings.TrimPrefix(fcNameText, "Free Company")
		fcNameText = strings.TrimSpace(fcNameText)
	}
	profile.FreeCompanyName = fcNameText

	// FC ID: extract from href inside character__freecompany__name
	fcLink := extractHref(html, `class="character__freecompany__name"`)
	if fcLink == "" {
		fcLink = extractHref(html, `class="character__freecompany__crest"`)
	}
	if m := fcIDRe.FindStringSubmatch(fcLink); len(m) >= 2 {
		profile.FreeCompanyID = m[1]
	}

	// Active Job: .character__class_icon img alt
	profile.ActiveJob = extractAlt(html, `class="character__class_icon"`)

	// ClassJobs: .character__level__list entries
	profile.ClassJobs = parseClassJobs(html, id)

	return profile, nil
}

// extractTextBetween finds text between a marker and an end tag.
func extractTextBetween(html, startMarker, endTag string) string {
	startIdx := strings.Index(html, startMarker)
	if startIdx == -1 {
		return ""
	}
	// Find the closing > of the opening tag.
	tagEnd := strings.Index(html[startIdx:], ">")
	if tagEnd == -1 {
		return ""
	}
	contentStart := startIdx + tagEnd + 1
	endIdx := strings.Index(html[contentStart:], endTag)
	if endIdx == -1 {
		return ""
	}
	return stripTags(html[contentStart : contentStart+endIdx])
}

// extractAllTextBetween finds all occurrences of text between markers.
func extractAllTextBetween(html, startMarker, endTag string) []string {
	var results []string
	searchFrom := 0
	for {
		startIdx := strings.Index(html[searchFrom:], startMarker)
		if startIdx == -1 {
			break
		}
		startIdx += searchFrom
		tagEnd := strings.Index(html[startIdx:], ">")
		if tagEnd == -1 {
			break
		}
		contentStart := startIdx + tagEnd + 1
		endIdx := strings.Index(html[contentStart:], endTag)
		if endIdx == -1 {
			break
		}
		results = append(results, stripTags(html[contentStart:contentStart+endIdx]))
		searchFrom = contentStart + endIdx + len(endTag)
	}
	return results
}

// extractAttr extracts an attribute from an element found by a marker.
func extractAttr(html, containerMarker, tagName, attr string) string {
	startIdx := strings.Index(html, containerMarker)
	if startIdx == -1 {
		return ""
	}
	// Find the tag within the container.
	tagStart := strings.Index(html[startIdx:], "<"+tagName)
	if tagStart == -1 {
		return ""
	}
	tagStart += startIdx
	tagEnd := strings.Index(html[tagStart:], ">")
	if tagEnd == -1 {
		return ""
	}
	tagHTML := html[tagStart : tagStart+tagEnd+1]
	return extractAttribute(tagHTML, attr)
}

// extractAlt extracts the alt attribute from an img within a container.
func extractAlt(html, containerMarker string) string {
	return extractAttr(html, containerMarker, "img", "alt")
}

// extractHref extracts the href attribute from an anchor within a container.
func extractHref(html, containerMarker string) string {
	return extractAttr(html, containerMarker, "a", "href")
}

// extractAttribute extracts a named attribute from an HTML tag string.
func extractAttribute(tagHTML, attr string) string {
	attrPattern := attr + `="`
	idx := strings.Index(tagHTML, attrPattern)
	if idx == -1 {
		return ""
	}
	start := idx + len(attrPattern)
	end := strings.Index(tagHTML[start:], `"`)
	if end == -1 {
		return ""
	}
	return tagHTML[start : start+end]
}

// parseClassJobs extracts class/job entries from the character profile HTML.
// The Lodestone renders jobs as <li><img data-tooltip="JobName">Level</li>
// inside a <div class="character__level__list"><ul>...</ul></div>.
func parseClassJobs(html string, charID uint32) []contract.ClassJobRecord {
	var jobs []contract.ClassJobRecord

	// Find the level list section.
	listIdx := strings.Index(html, `class="character__level__list"`)
	if listIdx == -1 {
		return jobs
	}

	// Extract each <li> entry within the list.
	searchFrom := listIdx
	for {
		liIdx := strings.Index(html[searchFrom:], "<li>")
		if liIdx == -1 {
			break
		}
		liIdx += searchFrom
		liEnd := strings.Index(html[liIdx:], "</li>")
		if liEnd == -1 {
			break
		}
		entryHTML := html[liIdx : liIdx+liEnd]

		// Job name from data-tooltip attribute on the <img> tag.
		jobName := extractAttribute(entryHTML, "data-tooltip")
		if jobName == "" {
			searchFrom = liIdx + liEnd + 1
			continue
		}

		// Strip "(Limited Job)" suffix (Blue Mage, Beastmaster).
		jobName = strings.TrimSuffix(jobName, " (Limited Job)")

		// Handle combined names like "Paladin / Gladiator" — use the first part.
		if idx := strings.Index(jobName, " / "); idx != -1 {
			jobName = jobName[:idx]
		}
		jobName = strings.TrimSpace(jobName)

		// Level is the text content after </img> (or after the last > of the img tag).
		// Format is either a plain number ("100") or "-" (unleveled).
		imgEnd := strings.LastIndex(entryHTML, ">")
		if imgEnd == -1 || imgEnd+1 >= len(entryHTML) {
			searchFrom = liIdx + liEnd + 1
			continue
		}
		levelStr := strings.TrimSpace(entryHTML[imgEnd+1:])
		if levelStr == "-" || levelStr == "" {
			searchFrom = liIdx + liEnd + 1
			continue
		}
		levelStr = strings.TrimPrefix(levelStr, "Lv.")
		levelStr = strings.TrimSpace(levelStr)

		level, _ := strconv.ParseUint(levelStr, 10, 8)

		classJobID, ok := lodestoneJobIDs[jobName]
		if !ok {
			// Unknown job name — skip to avoid inserting with class_job_id=0.
			searchFrom = liIdx + liEnd + 1
			continue
		}
		jobs = append(jobs, contract.ClassJobRecord{
			CharacterID: charID,
			ClassJobID:  classJobID,
			Name:        jobName,
			Level:       uint8(level),
		})

		searchFrom = liIdx + liEnd + 1
	}

	return jobs
}
