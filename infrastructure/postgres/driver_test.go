package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/postgrestest"
)

// TestMigrateUpWaitsForTheDatabaseMigrationLock holds the migration advisory
// lock from another session, as a second pod booting at the same time would:
// migrating must wait for it rather than run alongside.
func TestMigrateUpWaitsForTheDatabaseMigrationLock(t *testing.T) {
	driver := postgrestest.NewDriver(t)
	migrator, ok := driver.(interface {
		MigrateUp(context.Context) error
		MigrateDown(context.Context) error
	})
	if !ok {
		t.Fatal("the driver cannot migrate")
	}
	ctx := context.Background()
	// Start from an empty schema so there is something to apply.
	if err := migrator.MigrateDown(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := driver.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, postgres.MigrationLockID); err != nil {
		t.Fatal(err)
	}

	waiting, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	if err := migrator.MigrateUp(waiting); err == nil {
		t.Fatal("MigrateUp ran while another session held the migration lock")
	}

	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, postgres.MigrationLockID); err != nil {
		t.Fatal(err)
	}
	if err := migrator.MigrateUp(ctx); err != nil {
		t.Fatalf("MigrateUp after the lock was released: %v", err)
	}
	var held int
	// pg_locks is cluster-wide; other packages migrate their own temporary
	// databases concurrently, so count only this database's holders.
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = $1 AND objid::bigint = $2`,
		postgres.MigrationLockID>>32, postgres.MigrationLockID&0xffffffff).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("the migration lock is still held after MigrateUp (%d holders)", held)
	}
}
