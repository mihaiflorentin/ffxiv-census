package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/domain/proxy/worker"
	proxyinfra "github.com/mihaiflorentin88/ffxiv-census/infrastructure/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy",
	Short: "Proxy pool management (discover, scan, retire)",
}

var proxyDiscoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Fetch proxy lists directly from the providers and register new proxies in bulk",
	Long: `Fetch the configured providers' proxy lists straight from this host (never
through the proxy pool), insert the proxies the pool does not know yet with
bulk statements and mark every listed proxy as listed again, which revives a
retired one. --limit caps the proxies registered per run and is split fairly
across the providers; lists a provider reports as unchanged are skipped.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		logger := container.Load.Logger()

		logger.InfoContext(ctx, "proxy.discover.start")

		discoverer := container.Load.ProxyDiscoverer()
		if discoverer == nil {
			return fmt.Errorf("proxy discoverer not initialised")
		}

		limit, _ := cmd.Flags().GetInt("limit")
		if limit < 0 {
			limit = 0
		}

		res, err := discoverer.Run(ctx, limit)
		if err != nil {
			return err
		}

		logger.InfoContext(ctx, "proxy.discover.complete", "inserted", res.Inserted, "limit", limit, "providers", len(res.Providers))
		return nil
	},
}

// guardControlTimeout bounds each endpoint-guard control request; the guard
// only observes error versus nil.
const guardControlTimeout = 10 * time.Second

// runScanWorker wires the scan store, the general checker, the endpoint
// guard and the parsed [proxy.scan] policy, then runs the scan worker until
// the context is cancelled.
func runScanWorker(ctx context.Context, concurrency int) error {
	logger := container.Load.Logger()
	cfg := container.Load.Config().Proxy
	logger.InfoContext(ctx, "proxy.scan.start", "concurrency", concurrency)

	if concurrency <= 0 {
		return fmt.Errorf("proxy scan: concurrency must be positive, got %d", concurrency)
	}
	repo := container.Load.ProxyRepository()
	if repo == nil {
		return fmt.Errorf("proxy repository not initialised")
	}
	store, ok := repo.(contract.ProxyScanStore)
	if !ok {
		return fmt.Errorf("proxy repository does not support the scan store contract")
	}

	// The retirement sweep retires rows that were already over the thresholds
	// (the first pass after the feature ships covers ~1.85M rows in paced
	// batches) and rows that only crossed the age threshold with time. It
	// shares this command's lifecycle and never stops the scan on failure.
	interval, batch := container.Load.ProxyRetirementSweepSettings()
	sweep, err := newRetirementSweep(repo, container.Load.ProxyRetirementRule(), interval, batch, logger)
	if err != nil {
		return err
	}
	sweepCtx, stopSweep := context.WithCancel(ctx)
	defer stopSweep()
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		_ = sweep.Run(sweepCtx)
	}()

	// The endpoint guard is owned by this command's cancellable lifecycle:
	// it observes this replica's own egress with a bounded direct control
	// request and pauses claims while the endpoint is unhealthy.
	health := proxyinfra.NewHealthChecker(cfg.TestURL, cfg.TestTimeout, logger)
	guard := proxyinfra.NewEndpointGuard(func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, guardControlTimeout)
		defer cancel()
		return health.CheckDirect(ctx)
	}, cfg.Scan.ControlInterval, logger)
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- guard.Run(ctx)
	}()

	scanWorker := worker.NewScanWorker(store, container.Load.ProxyChecker(), guard,
		container.Load.ProxyScanPolicy(), container.Load.ProxyScanWeights(), logger)
	if hub := container.Load.ProxyHub(); hub != nil {
		scanWorker.SetNotifier(hub.NotifyAvailable)
	}

	err = scanWorker.RunScan(ctx, concurrency)
	stopSweep()
	<-sweepDone
	// RunScan returns only on cancellation, so the guard has observed the
	// same cancellation and its Run loop is winding down.
	if guardErr := <-guardDone; guardErr != nil {
		logger.ErrorContext(ctx, "proxy_scan.guard_exit", slog.Any("error", guardErr))
	}
	return err
}

// newRetirementSweep builds the retirement sweep over the repository's
// retirement store.
func newRetirementSweep(repo contract.ProxyRepository, rule contract.RetirementRule,
	interval time.Duration, batch int, logger contract.Logger,
) (*worker.RetirementSweep, error) {
	store, ok := repo.(contract.ProxyRetirementStore)
	if !ok {
		return nil, fmt.Errorf("proxy repository does not support the retirement store contract")
	}
	return worker.NewRetirementSweep(store, rule, interval, batch, logger), nil
}

var proxyScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Run a long-running proxy scan worker (guarded, lease-based)",
	Long: `Run a long-running scan worker that leases due proxy rows from the
verification, recovery and background queues and scans them within the
configured concurrency. Claims are bounded by free capacity and a claim
timeout shorter than the lease; the endpoint guard pauses claims while this
replica's own egress is unhealthy.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		concurrency, _ := cmd.Flags().GetInt("concurrency")

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		return runScanWorker(ctx, concurrency)
	},
}

var proxyRetireCmd = &cobra.Command{
	Use:   "retire",
	Short: "Retire proxies that never worked and kept failing (one full sweep, then exit)",
	Long: `Run one full retirement pass over the proxy table and exit. A proxy that
never worked, has failed at least [proxy.retire] after_failures times and was
first seen at least after_days ago is retired: the background scan skips it
and the row stays so discovery cannot re-insert it. The pass works in
sweep_batch-sized steps with a short rest between them, so it is safe to run
against a live database. The proxy scan command runs the same sweep on its
own at start and every sweep_interval; this command is for running it by hand.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger := container.Load.Logger()
		repo := container.Load.ProxyRepository()
		if repo == nil {
			return fmt.Errorf("proxy repository not initialised")
		}
		interval, batch := container.Load.ProxyRetirementSweepSettings()
		sweep, err := newRetirementSweep(repo, container.Load.ProxyRetirementRule(), interval, batch, logger)
		if err != nil {
			return err
		}
		retired, err := sweep.SweepOnce(ctx)
		logger.InfoContext(ctx, "proxy.retire.complete", "retired", retired)
		return err
	},
}

func init() {
	rootCmd.AddCommand(proxyCmd)

	proxyCmd.AddCommand(proxyDiscoverCmd)
	proxyCmd.AddCommand(proxyScanCmd)
	proxyCmd.AddCommand(proxyRetireCmd)

	proxyDiscoverCmd.Flags().IntP("limit", "l", 0, "max new proxies to register per run, split fairly across providers (0 = no limit)")
	proxyScanCmd.Flags().IntP("concurrency", "c", 4, "total per-replica scan concurrency (bounds each queue claim)")
}
