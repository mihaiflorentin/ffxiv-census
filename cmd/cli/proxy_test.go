package cli

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

type storeOnlyProxyRepository struct{ contract.ProxyRepository }

func TestNewRetirementSweep_RequiresRetirementStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rule := contract.RetirementRule{MinFailures: 10, MinAge: time.Hour}
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})

	sweep, err := newRetirementSweep(repo, rule, time.Hour, 100, logger)
	if err != nil || sweep == nil {
		t.Fatalf("newRetirementSweep = %v, %v; want a sweep for a repository that supports retirement", sweep, err)
	}
	if _, err := newRetirementSweep(storeOnlyProxyRepository{repo}, rule, time.Hour, 100, logger); err == nil {
		t.Fatal("a repository without a retirement store was accepted")
	}
}

func TestProxyRetireCmd_IsRegistered(t *testing.T) {
	found, _, err := proxyCmd.Find([]string{"retire"})
	if err != nil || found == nil || found.Name() != "retire" {
		t.Fatalf("proxy retire command missing: %v, %v", found, err)
	}
}

func TestProxyScanCmd_DeadScanPercentageFlagRemoved(t *testing.T) {
	if flag := proxyScanCmd.Flags().Lookup("dead-scan-percentage"); flag != nil {
		t.Fatalf("--dead-scan-percentage must be retired, got usage %q", flag.Usage)
	}
}

func TestProxyDiscoverCmd_InsertsDirectlyWithoutTheNewProxyQueueHop(t *testing.T) {
	found, _, err := proxyCmd.Find([]string{"discover"})
	if err != nil || found == nil || found.Name() != "discover" {
		t.Fatalf("proxy discover command missing: %v, %v", found, err)
	}
	if flag := found.Flags().Lookup("limit"); flag == nil {
		t.Fatal("proxy discover lost its --limit flag")
	}
	// Discovery registers proxies itself; the consumer for the retired
	// new-proxy event is gone.
	if sub, _, _ := proxyCmd.Find([]string{"consume"}); sub != nil && sub.Name() == "consume" {
		t.Fatal("proxy consume still registered although nothing publishes new-proxy events")
	}
}
