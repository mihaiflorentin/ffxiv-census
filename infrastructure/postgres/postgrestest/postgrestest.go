// Package postgrestest provides temporary-database helpers for tests that need
// a real Postgres.
package postgrestest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strconv"
	"testing"

	"github.com/mihaiflorentin88/ffxiv-census/config"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres"
	postgresmigration "github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/migration"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// NewDriver returns a database driver bound to a dedicated, uniquely
// named database that exists only for the duration of one test.
//
// Every test creates its own database (fixed prefix + random hex identifier),
// runs the full embedded migration set against it, and drops it in cleanup
// after closing all connections. No state is ever shared between tests, so
// no truncation is needed and tests cannot observe each other's rows.
//
// Connection and migration failures are fatal when REQUIRE_TEST_POSTGRES=1
// (CI / pre-merge verification); without it, an unreachable server skips the
// test so ordinary development runs stay green without integration
// prerequisites.
func NewDriver(t *testing.T) contract.DatabaseDriver {
	t.Helper()
	required := os.Getenv("REQUIRE_TEST_POSTGRES") == "1"
	// TEST_POSTGRES_PORT lets a developer point the tests at a Postgres on a
	// non-default local port (e.g. a dedicated container on 5433).
	port := postgresPort()

	ctx := context.Background()
	adminCfg := &config.PostgresConfig{
		Host:         "localhost",
		Port:         port,
		User:         "census",
		Password:     "secret",
		Database:     "postgres",
		SSLMode:      "disable",
		MaxOpenConns: 2,
		MaxIdleConns: 1,
	}
	admin, err := sql.Open("pgx", adminCfg.GetDSN())
	if err != nil {
		postgresUnavailable(t, required, err)
	}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		postgresUnavailable(t, required, err)
	}

	name, err := randomDatabaseName()
	if err != nil {
		t.Fatalf("generate test database name: %v", err)
	}
	// CREATE DATABASE cannot run inside a transaction.
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		_ = admin.Close()
		t.Fatalf("create test database %s: %v", name, err)
	}
	_ = admin.Close()

	cfg := &config.PostgresConfig{
		Host:         "localhost",
		Port:         port,
		User:         "census",
		Password:     "secret",
		Database:     name,
		SSLMode:      "disable",
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	}
	driver, err := postgres.NewDriver(cfg, postgresmigration.FS())
	if err != nil {
		t.Fatalf("migrate test database %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = driver.Close()
		drop, err := sql.Open("pgx", adminCfg.GetDSN())
		if err != nil {
			return
		}
		defer drop.Close()
		// WITH (FORCE) also terminates leaked connections (PG >= 13); the
		// plain fallback covers older servers.
		if _, err := drop.ExecContext(ctx, `DROP DATABASE `+name+` WITH (FORCE)`); err != nil {
			_, _ = drop.ExecContext(ctx, `DROP DATABASE `+name)
		}
	})
	return driver
}

func postgresUnavailable(t *testing.T, required bool, err error) {
	t.Helper()
	if required {
		t.Fatalf("test postgres required but unavailable: %v", err)
	}
	t.Skipf("postgres not available: %v", err)
}

// randomDatabaseName generates an identifier composed of a fixed prefix
// and hex characters only, so it is always a valid bare SQL identifier.
func randomDatabaseName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "census_test_" + hex.EncodeToString(b), nil
}

// postgresPort resolves the Postgres port repository tests connect to.
// An explicit TEST_POSTGRES_PORT wins when it parses as a positive integer;
// otherwise the default is 5433 — the dedicated test container port — so a
// developer's own 5432 server is never a fallback target.
func postgresPort() int {
	if v := os.Getenv("TEST_POSTGRES_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return 5433
}
