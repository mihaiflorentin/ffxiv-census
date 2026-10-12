package repository_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	postgresmigration "github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/migration"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// proxyIndexDefs returns indexname -> indexdef for the proxies table.
func proxyIndexDefs(t *testing.T, driver contract.DatabaseDriver) map[string]string {
	t.Helper()
	rows, err := driver.FetchMany(context.Background(),
		`SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'proxies'`)
	if err != nil {
		t.Fatalf("query proxy indexes: %v", err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}
	return defs
}

func TestProxyIndexMigration_RecordsRecoveryIndexesAndDropsLegacy(t *testing.T) {
	driver := newTestDriver(t)
	defs := proxyIndexDefs(t, driver)

	want := map[string][]string{
		"idx_proxies_recovery_claim": {
			"(next_attempt_at NULLS FIRST, id)",
			"INCLUDE (last_verified_at, scan_lease_until, scan_not_before)",
			"WHERE ((NOT general_healthy) AND (last_verified_at IS NOT NULL))",
		},
		"idx_proxies_recovery_recent": {
			"(last_verified_at)",
			"WHERE ((NOT general_healthy) AND (last_verified_at IS NOT NULL))",
		},
	}
	for name, parts := range want {
		def, ok := defs[name]
		if !ok {
			t.Errorf("index %s missing after migration", name)
			continue
		}
		for _, part := range parts {
			if !strings.Contains(def, part) {
				t.Errorf("index %s definition lacks %q:\n%s", name, part, def)
			}
		}
	}
	for _, name := range []string{"idx_proxies_available", "idx_proxies_scan_priority"} {
		if _, ok := defs[name]; ok {
			t.Errorf("legacy index %s still present", name)
		}
	}
}

// TestProxyIndexMigration_IsIdempotent replays the Up statements of the
// migration against a database that already has the indexes, as on
// production where they were created by hand.
func TestProxyIndexMigration_IsIdempotent(t *testing.T) {
	driver := newTestDriver(t)
	raw, err := postgresmigration.EmbedFS.ReadFile("query/00020_proxy_indexes.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	up := strings.SplitN(string(raw), "-- +goose Down", 2)[0]
	var stmts []string
	for _, s := range strings.Split(up, ";") {
		var lines []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "--") {
				lines = append(lines, l)
			}
		}
		if stmt := strings.TrimSpace(strings.Join(lines, "\n")); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	if len(stmts) == 0 {
		t.Fatal("no statements parsed from migration")
	}
	for range 2 {
		for _, s := range stmts {
			mustExec(t, driver, s)
		}
	}
	if _, ok := proxyIndexDefs(t, driver)["idx_proxies_recovery_claim"]; !ok {
		t.Error("recovery claim index missing after replay")
	}
}

func TestProxyIndexMigration_DownThenUp(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	if err := driver.MigrateDown(ctx); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := driver.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if _, ok := proxyIndexDefs(t, driver)["idx_proxies_recovery_recent"]; !ok {
		t.Error("recovery recent index missing after down/up cycle")
	}
}

// TestProxyQueries_WorkWithoutLegacyIndexes exercises every proxies read and
// claim path to prove none of them depended on the dropped indexes.
func TestProxyQueries_WorkWithoutLegacyIndexes(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	healthy := mustInsertProxy(t, repo, "10.2.0.1", 9001)
	recovering := mustInsertProxy(t, repo, "10.2.0.2", 9002)
	cold := mustInsertProxy(t, repo, "10.2.0.3", 9003)
	seedScanState(t, driver, healthy,
		`general_healthy = TRUE, status = 'active', latency_ms = 50, last_verified_at = statement_timestamp()`)
	seedScanState(t, driver, recovering,
		`general_healthy = FALSE, status = 'inactive', last_verified_at = statement_timestamp()`)
	seedScanState(t, driver, cold, `general_healthy = FALSE, status = 'dead'`)

	if ok, err := repo.Exists(ctx, "http", "10.2.0.1", 9001); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if n, err := repo.Count(ctx); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v", n, err)
	}
	active, err := repo.ListActive(ctx, 10)
	if err != nil || len(active) != 1 || active[0].ID != healthy {
		t.Fatalf("ListActive = %v, %v", active, err)
	}
	counts, err := repo.CountByStatus(ctx)
	if err != nil || counts["active"] != 1 || counts["inactive"] != 1 || counts["dead"] != 1 {
		t.Fatalf("CountByStatus = %v, %v", counts, err)
	}
	claimed, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || claimed == nil || claimed.ID != healthy {
		t.Fatalf("ClaimProxy = %v, %v", claimed, err)
	}
	if err := repo.ReleaseProxy(ctx, healthy, "owner"); err != nil {
		t.Fatalf("ReleaseProxy: %v", err)
	}

	for _, tc := range []struct {
		name  string
		queue contract.ScanQueue
		want  int64
	}{
		{"verification", contract.ScanVerification, healthy},
		{"recovery", contract.ScanRecovery, recovering},
	} {
		leases, err := repo.ClaimScans(ctx, tc.queue, 5)
		if err != nil || len(leases) != 1 || leases[0].Record.ID != tc.want {
			t.Errorf("%s ClaimScans = %v, %v; want id %d", tc.name, leases, err, tc.want)
		}
	}
	bg, err := repo.ClaimScans(ctx, contract.ScanBackground, 5)
	if err != nil || len(bg) != 1 { // the recovering row is already leased
		t.Errorf("background ClaimScans = %d leases, %v; want 1", len(bg), err)
	}
}

// TestClaimScansRecovery_GenericPlanUsesRecentIndex pins the recovery claim
// to the recent-verification index even when PostgreSQL falls back to a
// generic plan (pgx caches prepared statements, so after five executions the
// bind-parameter horizon is invisible to the planner and it walks the claim
// index in deadline order, filtering most of the table).
func TestClaimScansRecovery_GenericPlanUsesRecentIndex(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	// Many long-unverified rows with early deadlines, a few freshly verified.
	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, general_healthy,
			last_verified_at, next_attempt_at, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.9.' || (g / 250) || '.' || (g % 250), 9000, FALSE,
			now() - interval '10 days' - g * interval '1 second',
			now() - interval '5 days' - g * interval '1 second', 'test', now(), now(), now()
		FROM generate_series(1, 40000) g`)
	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, general_healthy,
			last_verified_at, next_attempt_at, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.8.0.' || g, 9000, FALSE,
			now() - g * interval '10 seconds', now() - interval '1 second', 'test', now(), now(), now()
		FROM generate_series(1, 100) g`)
	mustExec(t, driver, `ANALYZE proxies`)

	query, args, err := repo.ClaimQueryForTest(contract.ScanRecovery, 50)
	if err != nil {
		t.Fatalf("claim query: %v", err)
	}
	db, err := driver.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatalf("force generic plan: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `PREPARE claim_plan AS `+query); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// EXECUTE takes literals; the bind values here are ints, floats and a token.
	literals := make([]string, len(args))
	for i, a := range args {
		if str, ok := a.(string); ok {
			literals[i] = "'" + str + "'"
		} else {
			literals[i] = fmt.Sprint(a)
		}
	}
	rows, err := conn.QueryContext(ctx,
		`EXPLAIN (COSTS OFF) EXECUTE claim_plan(`+strings.Join(literals, ", ")+`)`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "idx_proxies_recovery_recent") {
		t.Errorf("recovery claim does not use idx_proxies_recovery_recent:\n%s", plan.String())
	}
}
