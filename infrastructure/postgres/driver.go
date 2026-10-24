package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"

	"github.com/mihaiflorentin88/ffxiv-census/config"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var migrateGlobalMu sync.Mutex

// MigrationLockID is the PostgreSQL session advisory lock every migration
// run holds (goose's default id), so pods booting at the same time apply
// migrations one after another instead of racing.
const MigrationLockID = lock.DefaultLockID

// Driver wraps a pooled *sql.DB and satisfies the DatabaseDriver contract.
// Migrations run automatically (goose Up) the first time the pool is opened.
type Driver struct {
	cfg   *config.PostgresConfig
	migFS fs.FS

	once sync.Once
	db   *sql.DB
	err  error
}

// NewDriver builds a lazy PostgreSQL driver. migrationsFS holds goose .sql files.
func NewDriver(cfg *config.PostgresConfig, migrationsFS fs.FS) (contract.DatabaseDriver, error) {
	if cfg == nil {
		return nil, errors.New("postgres config is nil")
	}
	if migrationsFS == nil {
		return nil, errors.New("postgres migrations fs is nil")
	}
	d := &Driver{cfg: cfg, migFS: migrationsFS}
	if err := d.initialise(context.Background()); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Driver) Acquire(ctx context.Context) (*sql.DB, error) {
	if err := d.initialise(ctx); err != nil {
		return nil, err
	}
	return d.db, nil
}

func (d *Driver) Close() error {
	if d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *Driver) Execute(ctx context.Context, query string, args ...any) (sql.Result, error) {
	db, err := d.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, query, args...)
}

func (d *Driver) FetchOne(ctx context.Context, query string, args ...any) (*sql.Row, error) {
	db, err := d.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return db.QueryRowContext(ctx, query, args...), nil
}

func (d *Driver) FetchMany(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	db, err := d.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return db.QueryContext(ctx, query, args...)
}

// MigrateUp applies all pending migrations.
func (d *Driver) MigrateUp(ctx context.Context) error {
	db, err := d.Acquire(ctx)
	if err != nil {
		return err
	}
	migrateGlobalMu.Lock()
	defer migrateGlobalMu.Unlock()

	provider, err := newMigrationProvider(db, d.migFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// MigrateDown rolls back all migrations.
func (d *Driver) MigrateDown(ctx context.Context) error {
	db, err := d.Acquire(ctx)
	if err != nil {
		return err
	}
	migrateGlobalMu.Lock()
	defer migrateGlobalMu.Unlock()

	provider, err := newMigrationProvider(db, d.migFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("goose down: %w", err)
	}
	return nil
}

func (d *Driver) initialise(ctx context.Context) error {
	d.once.Do(func() {
		d.err = d.migrateUp(ctx)
	})
	return d.err
}

func (d *Driver) migrateUp(ctx context.Context) error {
	dsn := d.cfg.GetDSN()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	d.db = db
	d.applyPoolSettings()

	migrateGlobalMu.Lock()
	defer migrateGlobalMu.Unlock()

	provider, err := newMigrationProvider(db, d.migFS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}

// A boot waits up to migrationLockTries x migrationLockRetry seconds (30
// minutes) for another process's migration run: long enough for a
// concurrent index build on the full characters table.
const (
	migrationLockRetry = 5
	migrationLockTries = 360
)

// newMigrationProvider builds the goose provider every migration run uses.
// It holds the MigrationLockID session advisory lock for the whole run, so
// concurrent boots serialise at the database: the second waits, then finds
// nothing left to apply.
func newMigrationProvider(db *sql.DB, migrations fs.FS) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(
		lock.WithLockID(MigrationLockID),
		lock.WithLockTimeout(migrationLockRetry, migrationLockTries),
	)
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(database.DialectPostgres, db, migrations, goose.WithSessionLocker(locker))
}

func (d *Driver) applyPoolSettings() {
	maxOpen := d.cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 10
	}
	d.db.SetMaxOpenConns(maxOpen)

	maxIdle := d.cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 5
	}
	d.db.SetMaxIdleConns(maxIdle)
	d.db.SetConnMaxLifetime(30 * time.Minute)
	d.db.SetConnMaxIdleTime(5 * time.Minute)
}
