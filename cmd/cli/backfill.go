package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// sleepFunc waits for d, or returns the context's error if it ends first.
type sleepFunc func(ctx context.Context, d time.Duration) error

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// runBackfillJobMasks runs backfill batches of batchSize until the walk is
// done, pausing between batches. Every batch commits its own progress, so an
// interruption loses nothing: the next run resumes from the stored cursor.
func runBackfillJobMasks(ctx context.Context, repo contract.JobMaskBackfillRepository, batchSize int, pause time.Duration, sleep sleepFunc, out io.Writer) error {
	if repo == nil {
		return errors.New("job mask backfill unavailable (check PostgreSQL configuration)")
	}
	if batchSize <= 0 {
		return fmt.Errorf("--batch-size must be positive, got %d", batchSize)
	}
	var scanned, filled int
	var cursor int64
	for {
		batch, err := repo.BackfillJobMasksBatch(ctx, batchSize)
		if err != nil {
			_, _ = fmt.Fprintf(out, "job mask backfill stopped: scanned=%d filled=%d cursor=%d\n", scanned, filled, cursor)
			return fmt.Errorf("job mask backfill: %w", err)
		}
		scanned += batch.Scanned
		filled += batch.Filled
		cursor = batch.Cursor
		if batch.Done {
			_, _ = fmt.Fprintf(out, "job mask backfill complete: scanned=%d filled=%d cursor=%d\n", scanned, filled, cursor)
			return nil
		}
		if err := sleep(ctx, pause); err != nil {
			_, _ = fmt.Fprintf(out, "job mask backfill stopped: scanned=%d filled=%d cursor=%d (rerun to resume)\n", scanned, filled, cursor)
			return err
		}
	}
}

var backfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Fill derived columns of rows stored before the columns existed",
	Args:  cobra.NoArgs,
}

var backfillJobMasksCmd = &cobra.Command{
	Use:   "job-masks",
	Short: "Fill the job bitmasks of characters stored before they existed",
	Long: `Fill the job bitmasks (jobs levelled, jobs at the level cap) of characters
whose masks are still NULL, from their stored jobs. Already-set masks are left
alone; characters re-censused meanwhile get their masks from the normal write path.

The command walks characters by ascending ID in keyset batches of --batch-size,
one short transaction per batch, and sleeps --pause between batches to keep the
load low. Progress is stored after every batch, so it is safe to stop (Ctrl-C)
and rerun: it resumes from the stored cursor.

It is not a migration and never runs at boot; run it once, during quiet hours,
after upgrading.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		batchSize, _ := cmd.Flags().GetInt("batch-size")
		pause, _ := cmd.Flags().GetDuration("pause")
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runBackfillJobMasks(ctx, container.Load.JobMaskBackfillRepository(), batchSize, pause, sleepContext, cmd.OutOrStdout())
	},
}

func init() {
	backfillJobMasksCmd.Flags().Int("batch-size", 1000, "characters walked per batch (one short transaction each)")
	backfillJobMasksCmd.Flags().Duration("pause", 200*time.Millisecond, "pause between batches, e.g. 200ms or 1s")
	rootCmd.AddCommand(backfillCmd)
	backfillCmd.AddCommand(backfillJobMasksCmd)
}
