package container

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/config"
)

func TestProxyScanPolicy_CarriesRetirementRuleFromConfig(t *testing.T) {
	svc := NewServiceContainer()
	retire := svc.Config().Proxy.Retire

	got := svc.ProxyScanPolicy().Retirement
	if got.MinFailures != retire.AfterFailures || got.MinAge != time.Duration(retire.AfterDays)*24*time.Hour {
		t.Fatalf("policy retirement = %+v, want %d failures and %d days", got, retire.AfterFailures, retire.AfterDays)
	}
	if got != svc.ProxyRetirementRule() {
		t.Fatalf("ProxyRetirementRule = %+v, differs from the policy rule %+v", svc.ProxyRetirementRule(), got)
	}
}

func TestProxyListingRule_CarriesRetireSettingsFromConfig(t *testing.T) {
	svc := NewServiceContainer()
	retire := svc.Config().Proxy.Retire

	got := svc.ProxyListingRule()
	if got.RewriteInterval != retire.ListedRewriteInterval || got.ReviveAfter != retire.ReviveAfter {
		t.Fatalf("ProxyListingRule = %+v, want rewrite %s and revive %s", got, retire.ListedRewriteInterval, retire.ReviveAfter)
	}
}

func TestProxyProviders_Enabled(t *testing.T) {
	svc := NewServiceContainer()
	// Force config load so we can inspect it.
	cfg := svc.Config()
	if cfg == nil || cfg.Proxy == nil {
		t.Skip("proxy config not available")
	}

	tests := []struct {
		name     string
		enabled  bool
		provider func() interface{}
	}{
		{"ProxyScrape", cfg.Proxy.Providers.ProxyScrape, func() interface{} { return svc.ProxyScrapeProvider() }},
		{"Geonode", cfg.Proxy.Providers.Geonode, func() interface{} { return svc.GeonodeProvider() }},
		{"PubProxy", cfg.Proxy.Providers.PubProxy, func() interface{} { return svc.PubProxyProvider() }},
		{"Proxifly", cfg.Proxy.Providers.Proxifly, func() interface{} { return svc.ProxiflyProvider() }},
		{"TheSpeedX", cfg.Proxy.Providers.TheSpeedX, func() interface{} { return svc.TheSpeedXProvider() }},
		{"Monosans", cfg.Proxy.Providers.Monosans, func() interface{} { return svc.MonosansProvider() }},
		{"Gfpcom", cfg.Proxy.Providers.Gfpcom, func() interface{} { return svc.GfpcomProvider() }},
		{"Thordata", cfg.Proxy.Providers.Thordata, func() interface{} { return svc.ThordataProvider() }},
		{"Hproxy", cfg.Proxy.Providers.Hproxy, func() interface{} { return svc.HproxyProvider() }},
		{"Sage520", cfg.Proxy.Providers.Sage520, func() interface{} { return svc.Sage520Provider() }},
		{"ErcinDedeoglu", cfg.Proxy.Providers.ErcinDedeoglu, func() interface{} { return svc.ErcinDedeogluProvider() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.provider()
			if tt.enabled && p == nil {
				t.Errorf("provider %s is enabled in config but accessor returned nil", tt.name)
			}
			if !tt.enabled && p != nil {
				t.Errorf("provider %s is disabled in config but accessor returned non-nil", tt.name)
			}
		})
	}
}

func TestProxyProviders_Disabled(t *testing.T) {
	// Override config with all providers disabled.
	cfg := &config.Config{
		Proxy: &config.ProxyConfig{
			Providers: config.ProxyProviderConfig{},
		},
	}
	svc := NewServiceContainer()
	svc.config = cfg

	providers := []struct {
		name     string
		provider func() interface{}
	}{
		{"Monosans", func() interface{} { return svc.MonosansProvider() }},
		{"Gfpcom", func() interface{} { return svc.GfpcomProvider() }},
		{"Thordata", func() interface{} { return svc.ThordataProvider() }},
		{"Hproxy", func() interface{} { return svc.HproxyProvider() }},
		{"Sage520", func() interface{} { return svc.Sage520Provider() }},
		{"ErcinDedeoglu", func() interface{} { return svc.ErcinDedeogluProvider() }},
	}

	for _, tt := range providers {
		t.Run(tt.name+"_nil_when_disabled", func(t *testing.T) {
			if p := tt.provider(); p != nil {
				t.Errorf("provider %s should be nil when disabled, got %v", tt.name, p)
			}
		})
	}
}
