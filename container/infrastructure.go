package container

import (
	"fmt"
	"os"
	"strings"
	"time"

	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/geonode"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/httpclient"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/lodestone"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres"
	postgresmigration "github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/migration"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/writer"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/provider"
	proxyinfra "github.com/mihaiflorentin88/ffxiv-census/infrastructure/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/proxyscrape"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/pubproxy"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/rabbitmq"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/textproxy"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type InfrastructureContainer struct {
	httpClient            contract.HTTPClient
	discoveryHTTPClient   contract.HTTPClient
	statsd                contract.StatsdClient
	prometheusRegistry    *metrics.Registry
	databaseDriver        contract.DatabaseDriver
	queue                 contract.Queue
	lodestoneClient       contract.LodestoneClient
	characterRepository   contract.CharacterRepository
	resultWriter          contract.ResultWriter
	achievementRepository contract.AchievementRepository
	censusRunRepository   contract.CensusRunRepository
	uiStatsRepository     contract.UIStatsRepository
	providerRateLimiter   contract.ProviderRateLimiter
	proxyRepository       contract.ProxyRepository
	proxyChecker          contract.ProxyChecker
	destinationChecker    *proxyinfra.Checker
	proxyScrapeProvider   contract.ProxyProvider
	geonodeProvider       contract.ProxyProvider
	pubProxyProvider      contract.ProxyProvider
	proxiflyProvider      contract.ProxyProvider
	theSpeedXProvider     contract.ProxyProvider
	monosansProvider      contract.ProxyProvider
	gfpcomProvider        contract.ProxyProvider
	thordataProvider      contract.ProxyProvider
	hproxyProvider        contract.ProxyProvider
	sage520Provider       contract.ProxyProvider
	ercindedeogluProvider contract.ProxyProvider
}

// Logger returns the process-wide structured logger (infrastructure/logging.Logger)
// as a contract.Logger, for injection into adapters and domain objects.
func (s *ServiceContainer) Logger() contract.Logger {
	return logging.Logger
}

func (s *ServiceContainer) HTTPClient() contract.HTTPClient {
	if s.infrastructure.httpClient != nil {
		return s.infrastructure.httpClient
	}
	client := httpclient.New(nil)
	s.infrastructure.httpClient = client
	return client
}

// DiscoveryHTTPClient returns an HTTPClient that routes requests through a
// rotating pool of active proxies for public proxy-list providers. Falls back
// to the direct client when no proxy is available. Must not be used for
// Lodestone APIs.
func (s *ServiceContainer) DiscoveryHTTPClient() contract.HTTPClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discoveryHTTPClientUnlocked()
}

// discoveryHTTPClientUnlocked returns the discovery HTTP client without acquiring
// the mutex. Callers that already hold s.mu MUST use this method to avoid deadlock.
func (s *ServiceContainer) discoveryHTTPClientUnlocked() contract.HTTPClient {
	if s.infrastructure.discoveryHTTPClient != nil {
		return s.infrastructure.discoveryHTTPClient
	}
	// Build the proxy hub using unlocked accessors since we already hold the lock.
	driver := s.databaseUnlocked()
	if driver == nil {
		return s.HTTPClient()
	}
	repo := s.infrastructure.proxyRepository
	if repo == nil {
		repo = repository.NewProxyRepository(driver, s.proxyScanPolicyUnlocked())
		s.infrastructure.proxyRepository = repo
	}
	lockTTL := 5 * time.Minute
	if cfg := s.configUnlocked().Proxy; cfg != nil && cfg.Consumer.LockTTL != "" {
		if d, err := time.ParseDuration(cfg.Consumer.LockTTL); err == nil && d > 0 {
			lockTTL = d
		}
	}
	checker := s.destinationCheckerUnlocked()
	hub := proxydomain.NewProxyHub(repo, lockTTL, checker, s.proxyConsumerCooldownUnlocked(), s.proxyScanPolicyUnlocked()).
		WithDestinationCooldownCap(s.proxyConsumerCooldownMaxUnlocked())
	if hub == nil {
		return s.HTTPClient()
	}
	s.infrastructure.discoveryHTTPClient = httpclient.NewRotatingProxyClient(hub, s.HTTPClient(), 30*time.Second)
	return s.infrastructure.discoveryHTTPClient
}

func (s *ServiceContainer) Statsd() contract.StatsdClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.statsd != nil {
		return s.infrastructure.statsd
	}
	cfg := s.configUnlocked().Metrics
	endpoint := ""
	prefix := "ffxiv-census"
	if cfg != nil {
		endpoint = cfg.Endpoint
		if cfg.Prefix != "" {
			prefix = cfg.Prefix
		}
	}
	if envEndpoint := os.Getenv("STATSD_ADDRESS"); envEndpoint != "" {
		endpoint = envEndpoint
	}
	if endpoint == "" {
		endpoint = "127.0.0.1:8125"
	}
	client, err := metrics.New(endpoint, prefix)
	if err != nil {
		logging.Error("container.metrics", fmt.Sprintf("failed to create statsd client: %v", err))
		return nil
	}
	s.infrastructure.statsd = client
	return client
}

func (s *ServiceContainer) PrometheusRegistry() *metrics.Registry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.prometheusRegistry != nil {
		return s.infrastructure.prometheusRegistry
	}
	reg := metrics.NewRegistry()
	s.infrastructure.prometheusRegistry = reg
	return reg
}

func (s *ServiceContainer) ProviderRateLimiter() contract.ProviderRateLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerRateLimiterUnlocked()
}

func (s *ServiceContainer) providerRateLimiterUnlocked() contract.ProviderRateLimiter {
	if s.infrastructure.providerRateLimiter != nil {
		return s.infrastructure.providerRateLimiter
	}
	limiter := provider.NewRateLimiter()
	s.infrastructure.providerRateLimiter = limiter
	return limiter
}

func (s *ServiceContainer) Database() contract.DatabaseDriver {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.databaseUnlocked()
}

func (s *ServiceContainer) Postgres() contract.DatabaseDriver {
	return s.Database()
}

func (s *ServiceContainer) SQLite() contract.DatabaseDriver {
	return s.Database()
}

func (s *ServiceContainer) databaseUnlocked() contract.DatabaseDriver {
	if s.infrastructure.databaseDriver != nil {
		return s.infrastructure.databaseDriver
	}
	cfg := s.configUnlocked().Postgres
	if cfg == nil {
		logging.Warn("container.postgres", "postgres config missing")
		return nil
	}
	driver, err := postgres.NewDriver(cfg, postgresmigration.FS())
	if err != nil {
		logging.Error("container.postgres", fmt.Sprintf("failed to create postgres driver: %v", err))
		return nil
	}
	s.infrastructure.databaseDriver = driver
	return driver
}

func (s *ServiceContainer) Queue() contract.Queue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.queue != nil {
		return s.infrastructure.queue
	}
	full := s.configUnlocked()
	cfg := full.RabbitMQ
	if cfg == nil {
		logging.Warn("container.queue", "rabbitmq config missing")
		return nil
	}
	maxAttempts := 5
	if full.Queue != nil && full.Queue.MaxAttempts > 0 {
		maxAttempts = full.Queue.MaxAttempts
	}
	var opts []rabbitmq.Option
	if full.Queue != nil && len(full.Queue.RetryTiers) > 0 {
		opts = append(opts, rabbitmq.WithRetryTiers(full.Queue.RetryTiers))
	}
	q, err := rabbitmq.New(cfg.GetURL(), s.Logger(), maxAttempts, opts...)
	if err != nil {
		logging.Error("container.queue", fmt.Sprintf("failed to create rabbitmq queue: %v", err))
		return nil
	}
	s.infrastructure.queue = q
	return q
}

// LodestoneRequestObserver returns an observer that counts Lodestone request
// outcomes per hashed proxy bucket in the Prometheus registry. Observers share
// the same underlying counter, so one per client is fine.
func (s *ServiceContainer) LodestoneRequestObserver() contract.LodestoneRequestObserver {
	return metrics.NewLodestoneRequestObserver(s.PrometheusRegistry())
}

func (s *ServiceContainer) LodestoneClient() contract.LodestoneClient {
	observer := s.LodestoneRequestObserver()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.lodestoneClient != nil {
		return s.infrastructure.lodestoneClient
	}
	cfg := s.configUnlocked().Lodestone
	if cfg == nil {
		logging.Warn("container.lodestone", "lodestone config missing")
		return nil
	}
	client, err := lodestone.NewCustomClient(cfg, s.Logger(), s.providerRateLimiterUnlocked(), lodestone.WithRequestObserver(observer))
	if err != nil {
		logging.Error("container.lodestone", fmt.Sprintf("failed to create lodestone client: %v", err))
		return nil
	}
	s.infrastructure.lodestoneClient = client
	return client
}

func (s *ServiceContainer) CharacterRepository() contract.CharacterRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.characterRepository != nil {
		return s.infrastructure.characterRepository
	}
	driver := s.databaseUnlocked()
	if driver == nil {
		logging.Warn("container.character_repository", "database driver unavailable")
		return nil
	}
	s.infrastructure.characterRepository = repository.NewCharacterRepository(driver)
	return s.infrastructure.characterRepository
}

// ResultWriter returns the per-process batching writer that stores finished
// census results, or nil when the database is unavailable. Consumers must
// Close it on shutdown, after the worker has stopped, to flush pending
// results.
func (s *ServiceContainer) ResultWriter() contract.ResultWriter {
	repo := s.CharacterRepository()
	if repo == nil {
		return nil
	}
	registry := s.PrometheusRegistry()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.resultWriter != nil {
		return s.infrastructure.resultWriter
	}
	var cfg writer.Config
	if wc := s.configUnlocked().Writer; wc != nil {
		cfg.BatchSize = wc.BatchSize
		cfg.FlushInterval = wc.FlushInterval
	}
	s.infrastructure.resultWriter = writer.New(repo, cfg, metrics.NewResultWriterObserver(registry))
	return s.infrastructure.resultWriter
}

func (s *ServiceContainer) AchievementRepository() contract.AchievementRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.achievementRepository != nil {
		return s.infrastructure.achievementRepository
	}
	driver := s.databaseUnlocked()
	if driver == nil {
		logging.Warn("container.achievement_repository", "database driver unavailable")
		return nil
	}
	s.infrastructure.achievementRepository = repository.NewAchievementRepository(driver)
	return s.infrastructure.achievementRepository
}

func (s *ServiceContainer) CensusRunRepository() contract.CensusRunRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.censusRunRepository != nil {
		return s.infrastructure.censusRunRepository
	}
	driver := s.databaseUnlocked()
	if driver == nil {
		logging.Warn("container.census_run_repository", "database driver unavailable")
		return nil
	}
	s.infrastructure.censusRunRepository = repository.NewCensusRunRepository(driver)
	return s.infrastructure.censusRunRepository
}

func (s *ServiceContainer) UIStatsRepository() contract.UIStatsRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.uiStatsRepository != nil {
		return s.infrastructure.uiStatsRepository
	}
	driver := s.databaseUnlocked()
	if driver == nil {
		logging.Warn("container.ui_stats_repository", "database driver unavailable")
		return nil
	}
	s.infrastructure.uiStatsRepository = repository.NewUIStatsRepository(driver)
	return s.infrastructure.uiStatsRepository
}

func (s *ServiceContainer) ProxyRepository() contract.ProxyRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.proxyRepository != nil {
		return s.infrastructure.proxyRepository
	}
	driver := s.databaseUnlocked()
	if driver == nil {
		logging.Warn("container.proxy_repository", "database driver unavailable")
		return nil
	}
	s.infrastructure.proxyRepository = repository.NewProxyRepository(driver, s.proxyScanPolicyUnlocked())
	return s.infrastructure.proxyRepository
}

// ProxyChecker returns the general availability checker: a strict proxied
// GET against the configured JSON IP echo target ([proxy] test_url). This is
// the checker the scan worker uses for every general check.
func (s *ServiceContainer) ProxyChecker() contract.ProxyChecker {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.proxyChecker != nil {
		return s.infrastructure.proxyChecker
	}
	cfg := s.configUnlocked().Proxy
	checker := proxyinfra.NewHealthChecker(cfg.TestURL, cfg.TestTimeout, s.Logger())
	s.infrastructure.proxyChecker = checker
	return checker
}

// DestinationChecker returns the Lodestone destination checker built from the
// [proxy.consumer] settings. It is used for handout-time validation only;
// random provider discovery never invokes it.
func (s *ServiceContainer) DestinationChecker() contract.ProxyChecker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destinationCheckerUnlocked()
}

func (s *ServiceContainer) destinationCheckerUnlocked() *proxyinfra.Checker {
	if s.infrastructure.destinationChecker != nil {
		return s.infrastructure.destinationChecker
	}
	cfg := s.configUnlocked().Proxy
	checker := proxyinfra.NewChecker(cfg.Consumer.TestURL, cfg.Consumer.TestTimeout, s.Logger())
	s.infrastructure.destinationChecker = checker
	return checker
}

// ProxyScanPolicy returns the single parsed scheduler policy shared by the
// repository adapter and the scan worker. DeadAfter and FailThreshold come
// from the retained [proxy] threshold settings.
func (s *ServiceContainer) ProxyScanPolicy() contract.ProxyScanPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxyScanPolicyUnlocked()
}

func (s *ServiceContainer) proxyScanPolicyUnlocked() contract.ProxyScanPolicy {
	cfg := s.configUnlocked().Proxy
	return contract.ProxyScanPolicy{
		VerifyInterval:    cfg.Scan.VerificationInterval,
		FreshnessTTL:      cfg.Scan.FreshnessTTL,
		RecoveryBase:      cfg.Scan.RecoveryBase,
		RecoveryCap:       cfg.Scan.RecoveryCap,
		RecoveryHorizon:   cfg.Scan.RecoveryHorizon,
		LeaseDuration:     cfg.Scan.LeaseDuration,
		MinRepeatInterval: cfg.Scan.MinRepeatInterval,
		InconclusiveRetry: cfg.Scan.InconclusiveRetry,
		DeadAfter:         time.Duration(cfg.DeadThresholdDays) * 24 * time.Hour,
		FailThreshold:     cfg.FailCountThreshold,
	}
}

// ProxyScanWeights returns the long-run capacity shares for the
// verification, recovery and background queues.
func (s *ServiceContainer) ProxyScanWeights() [3]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	scan := s.configUnlocked().Proxy.Scan
	return [3]int{scan.WeightVerification, scan.WeightRecovery, scan.WeightBackground}
}

func (s *ServiceContainer) ProxyScrapeProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.proxyScrapeProvider != nil {
		return s.infrastructure.proxyScrapeProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.ProxyScrape {
		return nil
	}
	s.infrastructure.proxyScrapeProvider = proxyscrape.New(s.discoveryHTTPClientUnlocked(), cfg.Providers.ProxyScrapeURL)
	return s.infrastructure.proxyScrapeProvider
}

func (s *ServiceContainer) GeonodeProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.geonodeProvider != nil {
		return s.infrastructure.geonodeProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Geonode {
		return nil
	}
	s.infrastructure.geonodeProvider = geonode.New(s.discoveryHTTPClientUnlocked(), cfg.Providers.GeonodeURL)
	return s.infrastructure.geonodeProvider
}

func (s *ServiceContainer) PubProxyProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.pubProxyProvider != nil {
		return s.infrastructure.pubProxyProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.PubProxy {
		return nil
	}
	s.infrastructure.pubProxyProvider = pubproxy.New(s.discoveryHTTPClientUnlocked(), cfg.Providers.PubProxyURL)
	return s.infrastructure.pubProxyProvider
}

func (s *ServiceContainer) ProxiflyProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.proxiflyProvider != nil {
		return s.infrastructure.proxiflyProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Proxifly {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.ProxiflyURL, "/")
	s.infrastructure.proxiflyProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "proxifly", map[string]string{
		"http":   base + "/http/data.txt",
		"socks4": base + "/socks4/data.txt",
		"socks5": base + "/socks5/data.txt",
	})
	return s.infrastructure.proxiflyProvider
}

func (s *ServiceContainer) TheSpeedXProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.theSpeedXProvider != nil {
		return s.infrastructure.theSpeedXProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.TheSpeedX {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.TheSpeedXURL, "/")
	s.infrastructure.theSpeedXProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "thespeedx", map[string]string{
		"http":   base + "/http.txt",
		"socks4": base + "/socks4.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.theSpeedXProvider
}

func (s *ServiceContainer) MonosansProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.monosansProvider != nil {
		return s.infrastructure.monosansProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Monosans {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.MonosansURL, "/")
	s.infrastructure.monosansProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "monosans", map[string]string{
		"http":   base + "/http.txt",
		"socks4": base + "/socks4.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.monosansProvider
}

func (s *ServiceContainer) GfpcomProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.gfpcomProvider != nil {
		return s.infrastructure.gfpcomProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Gfpcom {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.GfpcomURL, "/")
	s.infrastructure.gfpcomProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "gfpcom", map[string]string{
		"http":   base + "/http.txt",
		"socks4": base + "/socks4.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.gfpcomProvider
}

func (s *ServiceContainer) ThordataProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.thordataProvider != nil {
		return s.infrastructure.thordataProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Thordata {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.ThordataURL, "/")
	s.infrastructure.thordataProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "thordata", map[string]string{
		"http": base + "/http.txt",
	})
	return s.infrastructure.thordataProvider
}

func (s *ServiceContainer) HproxyProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.hproxyProvider != nil {
		return s.infrastructure.hproxyProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Hproxy {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.HproxyURL, "/")
	s.infrastructure.hproxyProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "hproxy", map[string]string{
		"http":   base + "/http.txt",
		"socks4": base + "/socks4.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.hproxyProvider
}

func (s *ServiceContainer) Sage520Provider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.sage520Provider != nil {
		return s.infrastructure.sage520Provider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.Sage520 {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.Sage520URL, "/")
	s.infrastructure.sage520Provider = textproxy.New(s.discoveryHTTPClientUnlocked(), "sage520", map[string]string{
		"http":   base + "/http.txt",
		"socks4": base + "/socks4.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.sage520Provider
}

func (s *ServiceContainer) ErcinDedeogluProvider() contract.ProxyProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.infrastructure.ercindedeogluProvider != nil {
		return s.infrastructure.ercindedeogluProvider
	}
	cfg := s.configUnlocked().Proxy
	if cfg == nil || !cfg.Providers.ErcinDedeoglu {
		return nil
	}
	base := strings.TrimRight(cfg.Providers.ErcinDedeogluURL, "/")
	s.infrastructure.ercindedeogluProvider = textproxy.New(s.discoveryHTTPClientUnlocked(), "ercindedeoglu", map[string]string{
		"http":   base + "/http.txt",
		"socks5": base + "/socks5.txt",
	})
	return s.infrastructure.ercindedeogluProvider
}

// ProxyHub creates a ProxyHub for consumer handout. The lock TTL and the
// destination cooldown floor are read from [proxy.consumer] config and
// handouts are validated with the destination checker; random provider
// discovery paths on the hub never invoke that checker.
func (s *ServiceContainer) ProxyHub() *proxydomain.ProxyHub {
	repo := s.ProxyRepository()
	if repo == nil {
		return nil
	}
	s.mu.Lock()
	lockTTL := 5 * time.Minute
	if cfg := s.configUnlocked().Proxy; cfg != nil && cfg.Consumer.LockTTL != "" {
		if d, err := time.ParseDuration(cfg.Consumer.LockTTL); err == nil && d > 0 {
			lockTTL = d
		}
	}
	hub := proxydomain.NewProxyHub(repo, lockTTL, s.destinationCheckerUnlocked(),
		s.proxyConsumerCooldownUnlocked(), s.proxyScanPolicyUnlocked()).
		WithDestinationCooldownCap(s.proxyConsumerCooldownMaxUnlocked())
	if cfg := s.configUnlocked().Proxy; cfg != nil {
		hub.WithLockRefreshInterval(cfg.Consumer.LockRefreshInterval)
	}
	s.mu.Unlock()
	return hub
}

// proxyConsumerCooldownMaxUnlocked returns the ceiling the destination
// cooldown doubles to from [proxy.consumer].cooldown_max, defaulting to
// fifteen minutes when unset.
func (s *ServiceContainer) proxyConsumerCooldownMaxUnlocked() time.Duration {
	if cfg := s.configUnlocked().Proxy; cfg != nil && cfg.Consumer.CooldownMax > 0 {
		return cfg.Consumer.CooldownMax
	}
	return 15 * time.Minute
}

// proxyConsumerCooldownUnlocked returns the destination cooldown floor from
// [proxy.consumer].cooldown, defaulting to one minute when unset.
func (s *ServiceContainer) proxyConsumerCooldownUnlocked() time.Duration {
	if cfg := s.configUnlocked().Proxy; cfg != nil && cfg.Consumer.Cooldown > 0 {
		return cfg.Consumer.Cooldown
	}
	return time.Minute
}
