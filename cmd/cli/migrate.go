package cli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/ffxiv-census/container"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func runMigrate(direction string) error {
	if direction != "up" && direction != "down" {
		return fmt.Errorf("invalid direction %q, use up or down", direction)
	}
	driver := container.Load.Database()
	if driver == nil {
		return fmt.Errorf("database driver not initialised (check config/postgres)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch direction {
	case "up":
		return driver.MigrateUp(ctx)
	case "down":
		return driver.MigrateDown(ctx)
	default:
		return fmt.Errorf("invalid direction %q, use up or down", direction)
	}
}

// pgQueueJob mirrors a row from the legacy queue_jobs table.
type pgQueueJob struct {
	ID          int64
	Type        string
	Payload     []byte
	Status      string
	Attempts    int
	MaxAttempts int
	LastError   string
}

// runMigrateQueue reads all non-done jobs from the PostgreSQL queue_jobs table
// and publishes them individually to RabbitMQ. After a successful migration it
// deletes the migrated rows.
func runMigrateQueue(dryRun bool) error {
	db := container.Load.Database()
	if db == nil {
		return fmt.Errorf("database driver not initialised (check config/postgres)")
	}
	q := container.Load.Queue()
	if q == nil {
		return fmt.Errorf("queue not initialised (check rabbitmq config)")
	}
	logger := container.Load.Logger()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. Fetch all non-done jobs.
	rows, err := db.FetchMany(ctx,
		`SELECT id, type, payload, status, attempts, max_attempts, COALESCE(last_error, '')
		 FROM queue_jobs
		 WHERE status IN ('pending', 'claimed', 'failed')
		 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("fetch queue_jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var jobs []pgQueueJob
	for rows.Next() {
		var j pgQueueJob
		if err := rows.Scan(&j.ID, &j.Type, &j.Payload, &j.Status, &j.Attempts, &j.MaxAttempts, &j.LastError); err != nil {
			return fmt.Errorf("scan queue_jobs row: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate queue_jobs: %w", err)
	}

	if len(jobs) == 0 {
		logger.InfoContext(ctx, "migrate.queue.no_jobs")
		return nil
	}

	// 2. Count by type and status for the summary.
	typeCounts := make(map[string]int)
	statusCounts := make(map[string]int)
	for _, j := range jobs {
		typeCounts[j.Type]++
		statusCounts[j.Status]++
	}

	logger.InfoContext(
		ctx, "migrate.queue.found",
		slog.Int("total", len(jobs)),
		slog.Any("by_type", typeCounts),
		slog.Any("by_status", statusCounts),
		slog.Bool("dry_run", dryRun),
	)

	if dryRun {
		logger.InfoContext(ctx, "migrate.queue.dry_run_complete")
		return nil
	}

	// 3. Publish each job to RabbitMQ individually.
	var published, failed int
	for _, j := range jobs {
		job := contract.QueueJob{
			Type:    j.Type,
			Payload: j.Payload,
		}
		if err := q.Publish(ctx, job); err != nil {
			logger.WarnContext(
				ctx, "migrate.queue.publish_error",
				slog.Int64("id", j.ID),
				slog.String("type", j.Type),
				slog.Any("error", err),
			)
			failed++
			continue
		}
		published++
	}

	logger.InfoContext(
		ctx, "migrate.queue.published",
		slog.Int("published", published),
		slog.Int("failed", failed),
	)

	if failed > 0 {
		return fmt.Errorf("failed to publish %d of %d jobs", failed, len(jobs))
	}

	// 4. Delete migrated rows.
	result, err := db.Execute(ctx,
		`DELETE FROM queue_jobs WHERE status != 'done'`)
	if err != nil {
		return fmt.Errorf("delete migrated queue_jobs: %w", err)
	}
	deleted, _ := result.RowsAffected()
	logger.InfoContext(
		ctx, "migrate.queue.cleanup",
		slog.Int64("deleted", deleted),
	)

	return nil
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Runs PostgreSQL schema migrations (up applies all pending; down rolls back all)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		direction, _ := cmd.Flags().GetString("direction")
		return runMigrate(direction)
	},
}

var migrateQueueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Migrate pending jobs from PostgreSQL queue_jobs table to RabbitMQ",
	Long: `One-shot migration command that reads all pending, claimed, and failed jobs
from the legacy PostgreSQL queue_jobs table and publishes them to RabbitMQ.

After a successful migration the source rows are deleted. Use --dry-run to
preview the migration without publishing or deleting anything.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		return runMigrateQueue(dryRun)
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
	migrateCmd.Flags().String("direction", "up", "Migration direction: up or down")
	migrateCmd.AddCommand(migrateQueueCmd)
	migrateQueueCmd.Flags().Bool("dry-run", false, "preview migration without publishing or deleting")
}
