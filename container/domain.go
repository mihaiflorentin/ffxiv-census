package container

import (
	"context"
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	proxyhandler "github.com/mihaiflorentin88/ffxiv-census/domain/proxy/handler"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/logging"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/metrics"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// DomainContainer wires domain services.
type DomainContainer struct {
	censusService  *census.Service
	uiStatsService *census.UIStatsService
	proxyService   *proxydomain.Service
}

func (s *ServiceContainer) CensusService() *census.Service {
	if s.domain.censusService != nil {
		return s.domain.censusService
	}
	achievements := s.AchievementRepository()
	if achievements == nil {
		logging.Warn("container.census", "database driver unavailable, census service disabled")
		return nil
	}
	svc := census.NewService(
		s.CharacterRepository(),
		achievements,
		s.CensusRunRepository(),
	)
	// Honor the configured activity window and expansions ([census] in config.toml).
	if c := s.Config().Census; c != nil {
		if c.ActivityWindowDays > 0 {
			svc.SetActivityWindow(time.Duration(c.ActivityWindowDays) * 24 * time.Hour)
		}
		var expansions []census.ExpansionConfig
		for _, exp := range c.Expansions {
			expansions = append(expansions, census.ExpansionConfig{
				Name:          exp.Name,
				Version:       exp.Version,
				FinalQuest:    exp.FinalQuest,
				Icon:          exp.Icon,
				LevelCap:      exp.LevelCap,
				AchievementID: exp.AchievementID,
			})
		}
		svc.SetConfig(c.MaxLevel, expansions)
	}
	// Seed the milestone registry (idempotent) so achievement processing never
	// runs against an empty registry.
	if err := svc.SyncMilestones(context.Background()); err != nil {
		logging.Error("container.census", fmt.Sprintf("failed to sync milestones: %v", err))
	}
	s.domain.censusService = svc
	return svc
}

func (s *ServiceContainer) UIStatsService() *census.UIStatsService {
	if s.domain.uiStatsService != nil {
		return s.domain.uiStatsService
	}
	repo := s.UIStatsRepository()
	if repo == nil {
		logging.Warn("container.ui_stats", "database driver unavailable, UI statistics disabled")
		return nil
	}
	cacheTTL := time.Minute
	staleWarning := 12 * time.Hour
	if cfg := s.Config().Census; cfg != nil && cfg.UIStats != nil {
		if parsed, err := time.ParseDuration(cfg.UIStats.CacheTTL); err == nil && parsed > 0 {
			cacheTTL = parsed
		}
		if parsed, err := time.ParseDuration(cfg.UIStats.StaleWarning); err == nil && parsed > 0 {
			staleWarning = parsed
		}
	}
	s.domain.uiStatsService = census.NewUIStatsService(repo, cacheTTL, staleWarning)
	s.domain.uiStatsService.SetObserver(metrics.NewUIStatsObserver(s.PrometheusRegistry()))
	return s.domain.uiStatsService
}

// Handlers returns a registry of ingest handlers, each wired to its
// dependencies. Handlers are stateless, so a fresh registry per call is fine.
// When the census service is unavailable (no database), an empty registry is
// returned and the worker reports "no handler registered" rather than panicking.
func (s *ServiceContainer) Handlers() *handler.Registry {
	reg := handler.NewRegistry()
	svc := s.CensusService()
	if svc == nil {
		return reg
	}
	s.registerCensusHandlers(reg, svc, s.LodestoneClient(), s.ProviderRateLimiter())
	return reg
}

// registerCensusHandlers registers the three census handlers, all storing
// through the one per-process result writer.
func (s *ServiceContainer) registerCensusHandlers(reg *handler.Registry, svc *census.Service, lodestoneClient contract.LodestoneClient, rateLimiter contract.ProviderRateLimiter) {
	w := s.ResultWriter()
	reg.Register(handler.EventIDSweep, handler.NewIDSweep(lodestoneClient, svc, s.Logger()).WithWriter(w))
	reg.Register(handler.EventAchievementCensus, handler.NewAchievementCensus(lodestoneClient, svc, s.Logger(), rateLimiter).WithWriter(w))
	reg.Register(handler.EventCharacterCensus, handler.NewCharacterCensus(lodestoneClient, svc, s.Logger()).WithWriter(w))
}

// ProxyService returns the ingestion-only proxy service: providers plus the
// repository. It performs no health checks — scanning is owned by the
// guarded scan worker.
func (s *ServiceContainer) ProxyService() *proxydomain.Service {
	if s.domain.proxyService != nil {
		return s.domain.proxyService
	}
	repo := s.ProxyRepository()
	if repo == nil {
		logging.Warn("container.proxy_service", "database driver unavailable, proxy service disabled")
		return nil
	}

	var providers []contract.ProxyProvider
	if p := s.ProxyScrapeProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.GeonodeProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.PubProxyProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.ProxiflyProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.TheSpeedXProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.MonosansProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.GfpcomProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.ThordataProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.HproxyProvider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.Sage520Provider(); p != nil {
		providers = append(providers, p)
	}
	if p := s.ErcinDedeogluProvider(); p != nil {
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		logging.Warn("container.proxy_service", "no proxy providers configured")
	}

	svc := proxydomain.NewService(providers, repo, s.Logger())
	s.domain.proxyService = svc
	return svc
}

func (s *ServiceContainer) ProxyHandlers() *proxyhandler.Registry {
	reg := proxyhandler.NewRegistry()
	svc := s.ProxyService()
	if svc == nil {
		return reg
	}
	reg.Register(proxyhandler.EventNewProxy, proxyhandler.NewNewProxy(svc, s.Logger()))
	return reg
}

// ProxyCensusHandlers returns a handler registry wired to the proxy-aware Lodestone
// client. Used by proxy-mode consumer goroutines. The provided client must route
// ALL requests through a proxy.
func (s *ServiceContainer) ProxyCensusHandlers(lodestoneClient contract.LodestoneClient, rateLimiter contract.ProviderRateLimiter) *handler.Registry {
	reg := handler.NewRegistry()
	svc := s.CensusService()
	if svc == nil {
		return reg
	}
	s.registerCensusHandlers(reg, svc, lodestoneClient, rateLimiter)
	return reg
}
