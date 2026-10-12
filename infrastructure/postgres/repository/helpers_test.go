package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/postgrestest"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// testScanPolicy is the canonical scheduler policy used by repository tests.
// It mirrors the values the scan dispatcher will run with in production.
func testScanPolicy() contract.ProxyScanPolicy {
	return contract.ProxyScanPolicy{
		VerifyInterval:    time.Minute,
		FreshnessTTL:      2 * time.Minute,
		RecoveryBase:      time.Minute,
		RecoveryCap:       15 * time.Minute,
		RecoveryHorizon:   time.Hour,
		LeaseDuration:     30 * time.Second,
		MinRepeatInterval: time.Second,
		InconclusiveRetry: time.Minute,
		DeadAfter:         48 * time.Hour,
		FailThreshold:     5,
	}
}

// newTestDriver returns a database driver bound to a dedicated temporary
// database; see postgrestest.NewDriver.
func newTestDriver(t *testing.T) contract.DatabaseDriver {
	t.Helper()
	return postgrestest.NewDriver(t)
}

// mustInsertProxy inserts a proxy and returns its assigned ID.
func mustInsertProxy(t *testing.T, repo contract.ProxyRepository, ip string, port int) int64 {
	t.Helper()
	id, inserted, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: ip, Port: port, Source: "test",
	})
	if err != nil {
		t.Fatalf("insert proxy %s: %v", ip, err)
	}
	if !inserted {
		t.Fatalf("proxy %s/%d not inserted", ip, port)
	}
	return id
}

// mustExec runs a write statement or fails the test.
func mustExec(t *testing.T, driver contract.DatabaseDriver, query string, args ...any) {
	t.Helper()
	if _, err := driver.Execute(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// seedScanState applies raw scheduling-state setters to one proxy. Setters use
// SQL-relative timestamps so boundary tests never depend on wall-clock sleeps.
func seedScanState(t *testing.T, driver contract.DatabaseDriver, id int64, setters string) {
	t.Helper()
	mustExec(t, driver, `UPDATE proxies SET `+setters+` WHERE id = $1`, id)
}

// seedLegacyActiveProxy inserts a row the way pre-scheduling writers did:
// historical status only, no scheduling columns. It returns the new ID.
func seedLegacyActiveProxy(t *testing.T, driver contract.DatabaseDriver, ip string) int64 {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	var id int64
	err = db.QueryRowContext(context.Background(), `
		INSERT INTO proxies (protocol, ip, port, status, source, first_seen_at, created_at, updated_at)
		VALUES ('http', $1, 80, 'active', 'test', statement_timestamp(), statement_timestamp(), statement_timestamp())
		RETURNING id`, ip).Scan(&id)
	if err != nil {
		t.Fatalf("seed legacy active proxy: %v", err)
	}
	return id
}

// scanUpdate builds a relative ScanUpdate carrying the canonical policy
// copies (freshness TTL, dead-after, fail threshold) for one outcome.
func scanUpdate(outcome contract.ScanOutcome, next, repeat time.Duration) contract.ScanUpdate {
	return contract.ScanUpdate{
		Outcome:       outcome,
		LatencyMS:     42,
		NextDelay:     next,
		RepeatDelay:   repeat,
		FreshnessTTL:  2 * time.Minute,
		DeadAfter:     48 * time.Hour,
		FailThreshold: 5,
	}
}

// mustGetProxy fetches a proxy or fails the test when it is missing.
func mustGetProxy(t *testing.T, repo contract.ProxyRepository, id int64) *contract.ProxyRecord {
	t.Helper()
	p, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get proxy %d: %v", id, err)
	}
	if p == nil {
		t.Fatalf("proxy %d missing", id)
	}
	return p
}
