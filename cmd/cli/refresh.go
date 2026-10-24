package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func runRefreshUIStatsWithService(ctx context.Context, svc *census.UIStatsService, opts contract.UIStatsRefreshOptions) error {
	if svc == nil {
		return errors.New("UI statistics service unavailable")
	}
	result, err := svc.Refresh(ctx, opts)
	if err != nil {
		return fmt.Errorf("refresh UI statistics: %w", err)
	}
	if result == nil {
		return errors.New("refresh UI statistics returned no result")
	}
	if result.Skipped {
		fmt.Println("UI statistics refresh skipped: another refresh is already running")
		return nil
	}
	fmt.Printf("UI statistics refresh complete: mode=%s generated_at=%s characters=%d duration=%s payload_bytes=%d\n",
		result.Mode, result.Snapshot.GeneratedAt.Format(time.RFC3339), result.Snapshot.SourceCharacters,
		result.Snapshot.RefreshDuration.Round(time.Millisecond), result.PayloadBytes)
	return nil
}

func parseRefreshMode(value string) (contract.UIStatsRefreshMode, error) {
	switch contract.UIStatsRefreshMode(value) {
	case "", contract.UIStatsRefreshFull:
		return contract.UIStatsRefreshFull, nil
	case contract.UIStatsRefreshIncremental:
		return contract.UIStatsRefreshIncremental, nil
	default:
		return "", fmt.Errorf("unknown --mode %q (want full or incremental)", value)
	}
}

func runRefreshUIStats(mode contract.UIStatsRefreshMode) error {
	stats := container.Load.UIStatsService()
	censusService := container.Load.CensusService()
	if stats == nil || censusService == nil {
		return errors.New("census/UI statistics service unavailable (check PostgreSQL configuration)")
	}
	timeout := 2 * time.Hour
	if cfg := container.Load.Config().Census; cfg != nil && cfg.UIStats != nil {
		if parsed, err := time.ParseDuration(cfg.UIStats.RefreshTimeout); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runRefreshUIStatsWithService(ctx, stats, uiStatsRefreshOptions(censusService, timeout, mode))
}

// uiStatsRefreshOptions builds a refresh request from the census service's
// configured windows, maximum level and expansions (the level bands).
func uiStatsRefreshOptions(svc *census.Service, timeout time.Duration, mode contract.UIStatsRefreshMode) contract.UIStatsRefreshOptions {
	return contract.UIStatsRefreshOptions{
		ActivitySince:           svc.ActivitySince(),
		NewCharactersWindowDays: census.WindowDays(svc.NewCharactersWindow()),
		MaxLevel:                svc.MaxLevel(),
		LevelBands:              census.LevelBands(svc.Expansions(), svc.MaxLevel()),
		Timeout:                 timeout,
		Mode:                    mode,
	}
}

var refreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh derived application read models",
	Args:  cobra.NoArgs,
}

var refreshUIStatsCmd = &cobra.Command{
	Use:   "ui-stats",
	Short: "Refresh the bounded UI and aggregate API statistics snapshot",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		value, _ := cmd.Flags().GetString("mode")
		mode, err := parseRefreshMode(value)
		if err != nil {
			return err
		}
		return runRefreshUIStats(mode)
	},
}

func init() {
	refreshUIStatsCmd.Flags().String("mode", string(contract.UIStatsRefreshFull),
		"refresh mode: full (rebuild everything) or incremental (adjust the previous snapshot)")
	rootCmd.AddCommand(refreshCmd)
	refreshCmd.AddCommand(refreshUIStatsCmd)
}
