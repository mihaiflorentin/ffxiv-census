package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish queue jobs (cronjob entrypoint)",
}

func computeIDSweepRange(from, to, count uint32, auto bool, maxIDFunc func() (uint32, error)) (uint32, uint32, error) {
	if count == 0 {
		return 0, 0, fmt.Errorf("--count / --batch-size must be > 0")
	}
	if auto || (from == 0 && to == 0) {
		if maxIDFunc == nil {
			return 0, 0, fmt.Errorf("maxIDFunc required when auto-discovering ID sweep range")
		}
		maxID, err := maxIDFunc()
		if err != nil {
			return 0, 0, fmt.Errorf("lookup max character id: %w", err)
		}
		from = maxID + 1
		to = maxID + count
	} else if from > 0 && to == 0 {
		to = from + count - 1
	} else if to > 0 && from == 0 {
		from = 1
	}
	if to < from {
		return 0, 0, fmt.Errorf("--to (%d) must be >= --from (%d)", to, from)
	}
	return from, to, nil
}

func buildIDSweepJobs(from, to, chunkSize uint32, source string) []contract.QueueJob {
	if chunkSize == 0 {
		chunkSize = 100
	}
	var jobs []contract.QueueJob
	for start := from; start <= to; {
		end := start + chunkSize - 1
		if end > to || end < start {
			end = to // last chunk, or uint32 overflow guard
		}
		b, _ := json.Marshal(handler.IDSweepPayload{From: start, To: end, Source: source})
		jobs = append(jobs, contract.QueueJob{
			Type:    handler.EventIDSweep,
			Payload: b,
		})
		if end == to {
			break
		}
		start = end + 1
	}
	return jobs
}

func buildGapSweepJobs(gaps [][2]uint32, chunkSize uint32, source string) []contract.QueueJob {
	var jobs []contract.QueueJob
	for _, g := range gaps {
		gapJobs := buildIDSweepJobs(g[0], g[1], chunkSize, source)
		jobs = append(jobs, gapJobs...)
	}
	return jobs
}

func publishAll(q contract.Queue, logger contract.Logger, ctx context.Context, jobs []contract.QueueJob) (int, error) {
	for i, job := range jobs {
		if err := q.Publish(ctx, job); err != nil {
			return i, fmt.Errorf("publish %q job %d of %d: %w", job.Type, i+1, len(jobs), err)
		}
	}
	logger.InfoContext(ctx, "publish.enqueued", slog.Int("count", len(jobs)))
	return len(jobs), nil
}

type idSweepCursor interface {
	IDSweepCursor(ctx context.Context) (uint32, error)
	AdvanceIDSweepCursor(ctx context.Context, expected, next uint32) error
}

func publishAutoIDSweep(ctx context.Context, cursor idSweepCursor, q contract.Queue, logger contract.Logger, count, chunkSize uint32, source string) error {
	if count == 0 {
		return fmt.Errorf("--count / --batch-size must be > 0")
	}
	from, err := cursor.IDSweepCursor(ctx)
	if err != nil {
		return fmt.Errorf("lookup id sweep cursor: %w", err)
	}
	if from == 0 || count > ^uint32(0)-from {
		return fmt.Errorf("id sweep range overflows uint32: from=%d count=%d", from, count)
	}
	to := from + count - 1
	next := to + 1
	jobs := buildIDSweepJobs(from, to, chunkSize, source)
	if _, err := publishAll(q, logger, ctx, jobs); err != nil {
		return err
	}
	if err := cursor.AdvanceIDSweepCursor(ctx, from, next); err != nil {
		return fmt.Errorf("advance id sweep cursor after publishing [%d,%d]: %w", from, to, err)
	}
	logger.InfoContext(
		ctx,
		"publish.id_sweep",
		slog.Bool("auto", true),
		slog.Bool("fill_gaps", false),
		slog.Uint64("from_id", uint64(from)),
		slog.Uint64("to_id", uint64(to)),
		slog.Uint64("next_id", uint64(next)),
		slog.Uint64("chunk_size", uint64(chunkSize)),
		slog.String("source", source),
		slog.Int("count", len(jobs)),
	)
	return nil
}

// fillGapsScanner is the repository surface the fill-gaps mode needs.
type fillGapsScanner interface {
	MaxID(ctx context.Context) (uint32, error)
	FindIDGapsAfter(ctx context.Context, afterID, maxID uint32, limit int) ([][2]uint32, error)
	FillGapsCursor(ctx context.Context) (uint32, error)
	AdvanceFillGapsCursor(ctx context.Context, expected, next uint32) error
}

// publishFillGapsIDSweep publishes at most count id-sweep jobs covering the
// oldest unscanned gaps after the persistent fill-gaps cursor. With minID > 0
// it runs a one-shot manual scan from minID instead and never touches the
// cursor. When the cursor reaches MaxID it wraps to 0 so the sweep restarts
// from the lowest gaps on the next run.
func publishFillGapsIDSweep(ctx context.Context, repo fillGapsScanner, q contract.Queue, logger contract.Logger, count, chunkSize uint32, source string, minID uint32) error {
	if count == 0 {
		return fmt.Errorf("--count must be > 0 in --fill-gaps mode")
	}

	maxID, err := repo.MaxID(ctx)
	if err != nil {
		return fmt.Errorf("lookup max character id for gap fill: %w", err)
	}
	if maxID == 0 {
		logger.InfoContext(ctx, "publish.id_sweep_gaps.none_found", slog.Uint64("max_id", 0))
		return nil
	}

	cursorMode := minID == 0
	after := minID
	var cursorBefore uint32
	if cursorMode {
		c, err := repo.FillGapsCursor(ctx)
		if err != nil {
			return fmt.Errorf("lookup fill gaps cursor: %w", err)
		}
		if c >= maxID {
			if err := repo.AdvanceFillGapsCursor(ctx, c, 0); err != nil {
				return fmt.Errorf("wrap fill gaps cursor: %w", err)
			}
			c = 0
		}
		cursorBefore = c
		after = c
	}

	gaps, err := repo.FindIDGapsAfter(ctx, after, maxID, int(count))
	if err != nil {
		return fmt.Errorf("find id gaps: %w", err)
	}
	if len(gaps) == 0 {
		logger.InfoContext(ctx, "publish.id_sweep_gaps.none_found", slog.Uint64("max_id", uint64(maxID)))
		return nil
	}

	jobs := buildGapSweepJobs(gaps, chunkSize, source)
	if uint32(len(jobs)) > count {
		jobs = jobs[:count]
	}

	published, pubErr := publishAll(q, logger, ctx, jobs)
	if cursorMode && published > 0 {
		var last handler.IDSweepPayload
		if err := json.Unmarshal(jobs[published-1].Payload, &last); err != nil {
			return fmt.Errorf("decode last published fill-gaps job: %w", err)
		}
		if err := repo.AdvanceFillGapsCursor(ctx, cursorBefore, last.To); err != nil {
			return fmt.Errorf("advance fill gaps cursor to %d: %w", last.To, err)
		}
	}
	return pubErr
}

var publishIDSweepCmd = &cobra.Command{
	Use:   "id-sweep",
	Short: "Publish id-sweep jobs covering an ID range (chunked, auto-discovery, gap-fill, daemon)",
	RunE: func(cmd *cobra.Command, args []string) error {
		auto, _ := cmd.Flags().GetBool("auto")
		from, _ := cmd.Flags().GetUint32("from")
		to, _ := cmd.Flags().GetUint32("to")
		count, _ := cmd.Flags().GetUint32("count")
		batchSize, _ := cmd.Flags().GetUint32("batch-size")
		if cmd.Flags().Changed("batch-size") {
			count = batchSize
		}
		chunkSize, _ := cmd.Flags().GetUint32("chunk-size")
		source, _ := cmd.Flags().GetString("source")
		fillGaps, _ := cmd.Flags().GetBool("fill-gaps")
		daemon, _ := cmd.Flags().GetBool("daemon")
		daemonInterval, _ := cmd.Flags().GetDuration("daemon-interval")
		minID, _ := cmd.Flags().GetUint32("min-id")
		if daemon && daemonInterval <= 0 {
			return fmt.Errorf("invalid --daemon-interval %v: must be positive", daemonInterval)
		}
		switch source {
		case "auto", "lodestone":
		default:
			return fmt.Errorf("invalid --source %q: must be one of 'auto', 'lodestone'", source)
		}

		repo := container.Load.CharacterRepository()
		if repo == nil {
			return fmt.Errorf("character repository not initialised")
		}
		q := container.Load.Queue()
		if q == nil {
			return fmt.Errorf("queue not initialised")
		}
		defer func() {
			if err := q.Close(); err != nil {
				container.Load.Logger().ErrorContext(cmd.Context(), "queue.close_error", slog.Any("error", err))
			}
		}()
		logger := container.Load.Logger()

		publishBatch := func() error {
			var jobs []contract.QueueJob
			if fillGaps {
				return publishFillGapsIDSweep(cmd.Context(), repo, q, logger, count, chunkSize, source, minID)
			} else if auto || (from == 0 && to == 0) {
				return publishAutoIDSweep(cmd.Context(), repo, q, logger, count, chunkSize, source)
			} else {
				maxIDFunc := func() (uint32, error) {
					return repo.MaxID(cmd.Context())
				}
				actualFrom, actualTo, err := computeIDSweepRange(from, to, count, auto, maxIDFunc)
				if err != nil {
					return err
				}
				jobs = buildIDSweepJobs(actualFrom, actualTo, chunkSize, source)
			}

			if len(jobs) == 0 {
				return nil
			}

			if _, err := publishAll(q, logger, cmd.Context(), jobs); err != nil {
				return err
			}
			logger.InfoContext(
				cmd.Context(), "publish.id_sweep",
				slog.Bool("fill_gaps", fillGaps),
				slog.Uint64("chunk_size", uint64(chunkSize)),
				slog.String("source", source),
				slog.Int("count", len(jobs)),
			)
			return nil
		}

		if !daemon {
			return publishBatch()
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger.InfoContext(
			ctx, "publish.id_sweep_daemon.started",
			slog.Duration("interval", daemonInterval),
			slog.Bool("fill_gaps", fillGaps),
			slog.String("source", source),
		)

		if err := publishBatch(); err != nil {
			logger.WarnContext(ctx, "publish.id_sweep_daemon.initial_sweep_error", slog.Any("error", err))
		}

		ticker := time.NewTicker(daemonInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logger.InfoContext(ctx, "publish.id_sweep_daemon.stopped")
				return nil
			case <-ticker.C:
				if err := publishBatch(); err != nil {
					logger.WarnContext(ctx, "publish.id_sweep_daemon.publish_error", slog.Any("error", err))
				}
			}
		}
	},
}

// characterCensusCutoff returns the cutoff time for a character-census publish.
// A zero or negative duration disables the age predicate (returns zero Time);
// a positive duration returns now minus the duration.
func characterCensusCutoff(now time.Time, olderThan time.Duration) time.Time {
	if olderThan <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(-olderThan)
}

// characterCensusOptions bounds one character-census publish run.
type characterCensusOptions struct {
	// OlderThan is the age filter for live characters; zero or negative
	// selects the oldest regardless of age.
	OlderThan time.Duration
	// Limit caps the live characters published.
	Limit int
	// DeletedLimit caps the deleted characters re-probed in this run; zero
	// disables the re-probe.
	DeletedLimit int
	// DeletedOlderThan is how long a character must have been deleted
	// (since the last time it was confirmed missing) before it is re-probed.
	DeletedOlderThan time.Duration
}

// publishCharacterCensus publishes character-census jobs for the oldest live
// characters, followed by a small batch of deleted characters due for a
// re-probe. Deleted characters are not part of the normal selection: a
// character that is still missing is stamped with a fresh deleted_at by its
// census, which keeps it out of the next re-probes for DeletedOlderThan,
// while one that came back is stored again (resurrection self-heals).
func publishCharacterCensus(
	ctx context.Context,
	repo contract.CharacterRepository,
	q contract.Queue,
	logger contract.Logger,
	now time.Time,
	opts characterCensusOptions,
) error {
	cutoff := characterCensusCutoff(now, opts.OlderThan)
	ids, err := repo.ListStaleIDs(ctx, cutoff, opts.Limit)
	if err != nil {
		return fmt.Errorf("list stale: %w", err)
	}
	stale := len(ids)
	deleted, err := repo.ListDeletedIDs(ctx, now.UTC().Add(-opts.DeletedOlderThan), opts.DeletedLimit)
	if err != nil {
		return fmt.Errorf("list deleted: %w", err)
	}
	ids = append(ids, deleted...)

	jobs := make([]contract.QueueJob, 0, len(ids))
	for _, id := range ids {
		jobs = append(jobs, handler.CharacterCensusJob(id))
	}
	if _, err := publishAll(q, logger, ctx, jobs); err != nil {
		return err
	}
	logger.InfoContext(
		ctx, "publish.character_census",
		slog.String("older_than", opts.OlderThan.String()),
		slog.Int("limit", opts.Limit),
		slog.Int("stale", stale),
		slog.Int("deleted_limit", opts.DeletedLimit),
		slog.Int("deleted", len(deleted)),
		slog.Int("count", len(jobs)),
	)
	return nil
}

var publishCharacterCensusCmd = &cobra.Command{
	Use:   "character-census",
	Short: "Publish character-census jobs for live characters (oldest-first or age-filtered) plus a small re-probe batch of deleted ones",
	RunE: func(cmd *cobra.Command, args []string) error {
		olderThan, _ := cmd.Flags().GetDuration("older-than")
		limit, _ := cmd.Flags().GetInt("limit")
		deletedLimit, _ := cmd.Flags().GetInt("deleted-limit")
		deletedOlderThan, _ := cmd.Flags().GetDuration("deleted-older-than")
		if limit <= 0 {
			return fmt.Errorf("--limit must be positive")
		}
		if deletedLimit < 0 {
			return fmt.Errorf("--deleted-limit must not be negative")
		}
		repo := container.Load.CharacterRepository()
		if repo == nil {
			return fmt.Errorf("character repository not initialised")
		}
		q := container.Load.Queue()
		if q == nil {
			return fmt.Errorf("queue not initialised")
		}
		defer func() {
			if err := q.Close(); err != nil {
				container.Load.Logger().ErrorContext(cmd.Context(), "queue.close_error", slog.Any("error", err))
			}
		}()
		return publishCharacterCensus(cmd.Context(), repo, q, container.Load.Logger(), time.Now(), characterCensusOptions{
			OlderThan:        olderThan,
			Limit:            limit,
			DeletedLimit:     deletedLimit,
			DeletedOlderThan: deletedOlderThan,
		})
	},
}

var publishAchievementCensusCmd = &cobra.Command{
	Use:   "achievement-census",
	Short: "Publish achievement-census jobs for characters",
	RunE: func(cmd *cobra.Command, args []string) error {
		charID, _ := cmd.Flags().GetUint32("character-id")
		limit, _ := cmd.Flags().GetInt("limit")

		q := container.Load.Queue()
		if q == nil {
			return fmt.Errorf("queue not initialised")
		}
		defer func() {
			if err := q.Close(); err != nil {
				container.Load.Logger().ErrorContext(cmd.Context(), "queue.close_error", slog.Any("error", err))
			}
		}()

		if charID > 0 {
			job := handler.AchievementCensusJob(charID)
			if err := q.Publish(cmd.Context(), job); err != nil {
				return err
			}
			container.Load.Logger().InfoContext(
				cmd.Context(), "publish.achievement_census",
				slog.Uint64("character_id", uint64(charID)),
			)
			return nil
		}

		if limit <= 0 {
			limit = 1000
		}
		repo := container.Load.CharacterRepository()
		if repo == nil {
			return fmt.Errorf("character repository not initialised")
		}
		chars, err := repo.List(cmd.Context(), contract.CharacterFilter{}, limit, 0)
		if err != nil {
			return fmt.Errorf("list characters: %w", err)
		}

		var jobs []contract.QueueJob
		for _, c := range chars {
			jobs = append(jobs, handler.AchievementCensusJob(c.ID))
		}

		if _, err := publishAll(q, container.Load.Logger(), cmd.Context(), jobs); err != nil {
			return err
		}
		container.Load.Logger().InfoContext(
			cmd.Context(), "publish.achievement_census",
			slog.Int("limit", limit),
			slog.Int("found", len(chars)),
			slog.Int("count", len(jobs)),
		)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(publishCmd)
	publishCmd.AddCommand(publishIDSweepCmd)
	publishIDSweepCmd.Flags().Bool("auto", false, "automatically determine next ID range starting from max_id + 1")
	publishIDSweepCmd.Flags().Uint32("batch-size", 1000, "number of character IDs to sweep (alias for --count)")
	publishIDSweepCmd.Flags().Uint32("from", 0, "first character ID (default: max_id + 1)")
	publishIDSweepCmd.Flags().Uint32("to", 0, "last character ID (default: from + count - 1)")
	publishIDSweepCmd.Flags().Uint32("count", 1000, "number of character IDs to sweep when --to is omitted")
	publishIDSweepCmd.Flags().Uint32("chunk-size", 100, "IDs per id-sweep job")
	publishIDSweepCmd.Flags().String("source", "auto", "ingest source (auto, lodestone)")
	publishIDSweepCmd.Flags().Bool("fill-gaps", false, "scan unscanned holes between 1 and MaxID")
	publishIDSweepCmd.Flags().Bool("daemon", false, "run continuous auto-sweep loop")
	publishIDSweepCmd.Flags().Duration("daemon-interval", 30*time.Second, "tick interval for daemon checks")
	publishIDSweepCmd.Flags().Uint32("min-id", 0, "manual gap scan start for --fill-gaps (skips the persistent cursor)")
	publishCmd.AddCommand(publishCharacterCensusCmd)
	publishCharacterCensusCmd.Flags().Duration("older-than", 0, "positive duration filters by age; zero or negative selects oldest entries without age cutoff")
	publishCharacterCensusCmd.Flags().Int("limit", 1000, "max live characters to enqueue")
	publishCharacterCensusCmd.Flags().Int("deleted-limit", 10, "max deleted characters to re-probe per run (0 disables the re-probe)")
	publishCharacterCensusCmd.Flags().Duration("deleted-older-than", 30*24*time.Hour, "re-probe a deleted character only once it has been deleted this long")
	publishCmd.AddCommand(publishAchievementCensusCmd)
	publishAchievementCensusCmd.Flags().Uint32("character-id", 0, "specific character ID to census")
	publishAchievementCensusCmd.Flags().Int("limit", 1000, "max characters to enqueue")
}
