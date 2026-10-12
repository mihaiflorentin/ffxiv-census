package repository_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	postgresmigration "github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/migration"
	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestProxyRetirementMigration_AddsColumnsAndLiveBackgroundIndex(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()

	rows, err := driver.FetchMany(ctx,
		`SELECT column_name, is_nullable, data_type
		FROM information_schema.columns WHERE table_name = 'proxies'`)
	if err != nil {
		t.Fatalf("query proxy columns: %v", err)
	}
	defer rows.Close()
	got := map[string][2]string{}
	for rows.Next() {
		var name, nullable, dataType string
		if err := rows.Scan(&name, &nullable, &dataType); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		got[name] = [2]string{nullable, dataType}
	}
	for _, col := range []string{"retired_at", "last_listed_at"} {
		c, ok := got[col]
		if !ok {
			t.Errorf("proxies.%s missing after migration", col)
			continue
		}
		if c[0] != "YES" || c[1] != "timestamp with time zone" {
			t.Errorf("proxies.%s nullable=%q type=%q, want nullable timestamptz", col, c[0], c[1])
		}
	}

	def, ok := proxyIndexDefs(t, driver)["idx_proxies_background_live"]
	if !ok {
		t.Fatal("idx_proxies_background_live missing after migration")
	}
	for _, part := range []string{
		"(last_completed_at NULLS FIRST, id)",
		"WHERE ((NOT general_healthy) AND (retired_at IS NULL))",
	} {
		if !strings.Contains(def, part) {
			t.Errorf("idx_proxies_background_live lacks %q:\n%s", part, def)
		}
	}
}

// TestProxyRetirementMigration_IsIdempotent replays the Up statements against
// a database that already carries the columns and the index.
func TestProxyRetirementMigration_IsIdempotent(t *testing.T) {
	driver := newTestDriver(t)
	raw, err := postgresmigration.EmbedFS.ReadFile("query/00021_proxy_retirement.sql")
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
	if _, ok := proxyIndexDefs(t, driver)["idx_proxies_background_live"]; !ok {
		t.Error("live background index missing after replay")
	}
}

func TestProxyRetirementMigration_DownThenUp(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	if err := driver.MigrateDown(ctx); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := driver.MigrateUp(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if _, ok := proxyIndexDefs(t, driver)["idx_proxies_background_live"]; !ok {
		t.Error("live background index missing after down/up cycle")
	}
}

// TestClaimScansBackground_ConcurrentClaimersSkipRetiredAndNeverOverlap runs
// several claimers (two repositories, as two replicas) against a table where
// a fifth of the rows are retired, while discovery keeps listing endpoints.
func TestClaimScansBackground_ConcurrentClaimersSkipRetiredAndNeverOverlap(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repos := []*repository.ProxyRepository{
		repository.NewProxyRepository(driver, testScanPolicy()),
		repository.NewProxyRepository(driver, testScanPolicy()),
	}

	const total = 200
	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, retired_at, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.4.' || (g / 250) || '.' || (g % 250), 9000,
			CASE WHEN g % 5 = 0 THEN now() END, 'test', now(), now(), now()
		FROM generate_series(1, $1::int) g`, total)
	var retiredIDs []int64
	rows, err := driver.FetchMany(ctx, `SELECT id FROM proxies WHERE retired_at IS NOT NULL`)
	if err != nil {
		t.Fatalf("list retired: %v", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		retiredIDs = append(retiredIDs, id)
	}
	rows.Close()
	retired := map[int64]bool{}
	for _, id := range retiredIDs {
		retired[id] = true
	}

	var mu sync.Mutex
	claimed := map[int64]int{}
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := repos[worker%len(repos)]
			for {
				leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 7)
				if err != nil {
					t.Errorf("ClaimScans: %v", err)
					return
				}
				if len(leases) == 0 {
					return
				}
				mu.Lock()
				for _, l := range leases {
					claimed[l.Record.ID]++
				}
				mu.Unlock()
			}
		}()
	}
	// Discovery re-lists the retired endpoints concurrently with the claims.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for g := 5; g <= total; g += 5 {
			ep := endpoint(fmt.Sprintf("10.4.%d.%d", g/250, g%250), 9000)
			if _, err := repos[0].MarkListed(ctx, []contract.ProxyEndpoint{ep}); err != nil {
				t.Errorf("MarkListed: %v", err)
				return
			}
		}
	}()
	wg.Wait()

	live := 0
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("proxy %d claimed %d times", id, n)
		}
		if !retired[id] {
			live++
		}
	}
	if live != total-len(retiredIDs) {
		t.Errorf("claimed %d live proxies, want %d", live, total-len(retiredIDs))
	}
	// A retired row can only be claimed after discovery revived it.
	for id := range claimed {
		if retired[id] {
			if mustGetProxy(t, repos[0], id).RetiredAt != nil {
				t.Errorf("retired proxy %d was claimed while still retired", id)
			}
		}
	}
}

// TestClaimScansBackground_UsesLiveCandidateIndex pins the background claim
// to the partial index of live candidates, in a table where retired rows
// dominate, even under a generic plan.
func TestClaimScansBackground_UsesLiveCandidateIndex(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, general_healthy, retired_at,
			last_completed_at, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.9.' || (g / 250) || '.' || (g % 250), 9000, FALSE, now(),
			now() - g * interval '1 second', 'test', now(), now(), now()
		FROM generate_series(1, 40000) g`)
	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, general_healthy,
			last_completed_at, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.8.0.' || g, 9000, FALSE,
			now() - g * interval '10 seconds', 'test', now(), now(), now()
		FROM generate_series(1, 100) g`)
	mustExec(t, driver, `ANALYZE proxies`)

	query, args, err := repo.ClaimQueryForTest(contract.ScanBackground, 50)
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
	if _, err := conn.ExecContext(ctx, `PREPARE bg_plan AS `+query); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	literals := make([]string, len(args))
	for i, a := range args {
		if str, ok := a.(string); ok {
			literals[i] = "'" + str + "'"
		} else {
			literals[i] = fmt.Sprint(a)
		}
	}
	rows, err := conn.QueryContext(ctx,
		`EXPLAIN (COSTS OFF) EXECUTE bg_plan(`+strings.Join(literals, ", ")+`)`)
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
	if !strings.Contains(plan.String(), "idx_proxies_background_live") {
		t.Errorf("background claim does not use idx_proxies_background_live:\n%s", plan.String())
	}
}

// retireRule is the production default: ten failures and fourteen days.
var retireRule = contract.RetirementRule{MinFailures: 10, MinAge: 14 * 24 * time.Hour}

// failBackgroundScan claims the single proxy and completes the scan as a
// conclusive failure carrying the given retirement rule.
func failBackgroundScan(t *testing.T, repo *repository.ProxyRepository, rule contract.RetirementRule) {
	t.Helper()
	ctx := context.Background()
	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 5)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClaimScans = %d leases, %v; want 1", len(leases), err)
	}
	update := scanUpdate(contract.ScanFailure, time.Minute, time.Second)
	update.Retirement = rule
	ok, err := repo.CompleteScan(ctx, leases[0], update)
	if err != nil || !ok {
		t.Fatalf("CompleteScan = %v, %v; want accepted", ok, err)
	}
}

func TestCompleteScanFailure_RetiresOnlyProxiesThatNeverWorked(t *testing.T) {
	cases := []struct {
		name    string
		setters string // state before the failing scan
		rule    contract.RetirementRule
		want    bool
	}{
		{
			name:    "never worked, tenth failure, old enough",
			setters: `fail_count = 9, first_seen_at = now() - interval '15 days'`,
			rule:    retireRule, want: true,
		},
		{
			name:    "never worked, exactly the age threshold",
			setters: `fail_count = 9, first_seen_at = now() - interval '14 days'`,
			rule:    retireRule, want: true,
		},
		{
			name:    "never worked, ninth failure",
			setters: `fail_count = 8, first_seen_at = now() - interval '15 days'`,
			rule:    retireRule, want: false,
		},
		{
			name:    "never worked, enough failures but too young",
			setters: `fail_count = 30, first_seen_at = now() - interval '13 days'`,
			rule:    retireRule, want: false,
		},
		{
			name:    "worked once, many failures, old",
			setters: `fail_count = 30, last_alive_at = now() - interval '20 days', first_seen_at = now() - interval '40 days'`,
			rule:    retireRule, want: false,
		},
		{
			name:    "retirement disabled",
			setters: `fail_count = 30, first_seen_at = now() - interval '40 days'`,
			rule:    contract.RetirementRule{}, want: false,
		},
		{
			name:    "custom thresholds",
			setters: `fail_count = 2, first_seen_at = now() - interval '2 days'`,
			rule:    contract.RetirementRule{MinFailures: 3, MinAge: 24 * time.Hour}, want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := newTestDriver(t)
			repo := repository.NewProxyRepository(driver, testScanPolicy())
			id := mustInsertProxy(t, repo, "10.6.0.1", 9001)
			seedScanState(t, driver, id, tc.setters)

			failBackgroundScan(t, repo, tc.rule)

			p := mustGetProxy(t, repo, id)
			if got := p.RetiredAt != nil; got != tc.want {
				t.Fatalf("retired = %v, want %v (fail_count %d)", got, tc.want, p.FailCount)
			}
		})
	}
}

func TestCompleteScanInconclusive_NeverRetires(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.6.0.2", 9001)
	seedScanState(t, driver, id, `fail_count = 50, first_seen_at = now() - interval '40 days'`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 5)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClaimScans = %d, %v", len(leases), err)
	}
	update := scanUpdate(contract.ScanInconclusive, time.Minute, time.Second)
	update.Retirement = retireRule
	if ok, err := repo.CompleteScan(ctx, leases[0], update); err != nil || !ok {
		t.Fatalf("CompleteScan = %v, %v", ok, err)
	}
	if p := mustGetProxy(t, repo, id); p.RetiredAt != nil {
		t.Fatalf("inconclusive scan retired the proxy at %v", p.RetiredAt)
	}
}

func TestCompleteScanSuccess_UnretiresProxy(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.6.0.3", 9001)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 5)
	if err != nil || len(leases) != 1 {
		t.Fatalf("ClaimScans = %d, %v", len(leases), err)
	}
	// Retired while the scan was in flight (the periodic sweep ran).
	seedScanState(t, driver, id, `retired_at = statement_timestamp()`)
	if ok, err := repo.CompleteScan(ctx, leases[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil || !ok {
		t.Fatalf("CompleteScan = %v, %v", ok, err)
	}
	p := mustGetProxy(t, repo, id)
	if p.RetiredAt != nil {
		t.Fatalf("a proxy that just worked is still retired (%v)", p.RetiredAt)
	}
	if !p.GeneralHealthy {
		t.Fatal("proxy not healthy after successful scan")
	}
}

func TestCompleteScanFailure_KeepsExistingRetirementTime(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.6.0.4", 9001)
	seedScanState(t, driver, id,
		`fail_count = 20, first_seen_at = now() - interval '30 days', retired_at = now() - interval '5 days'`)
	// A retired row is not claimed by the background queue, so lease it by hand
	// the way an in-flight scan holds it.
	seedScanState(t, driver, id,
		`scan_token = 'tok', scan_lease_until = now() + interval '1 minute', claimed_version = observation_version`)
	before := mustGetProxy(t, repo, id)

	lease := contract.ScanLease{Record: *before, Token: "tok", Version: before.ObservationVersion}
	update := scanUpdate(contract.ScanFailure, time.Minute, time.Second)
	update.Retirement = retireRule
	if ok, err := repo.CompleteScan(context.Background(), lease, update); err != nil || !ok {
		t.Fatalf("CompleteScan = %v, %v", ok, err)
	}
	after := mustGetProxy(t, repo, id)
	if after.RetiredAt == nil || !after.RetiredAt.Equal(*before.RetiredAt) {
		t.Fatalf("retired_at = %v, want unchanged %v", after.RetiredAt, before.RetiredAt)
	}
}

// sweepAll runs RetireStale to completion and returns the total retired.
func sweepAll(t *testing.T, repo *repository.ProxyRepository, rule contract.RetirementRule, batch int) (total int64, calls int) {
	t.Helper()
	var after int64
	for {
		res, err := repo.RetireStale(context.Background(), rule, after, batch)
		if err != nil {
			t.Fatalf("RetireStale: %v", err)
		}
		calls++
		total += res.Retired
		if res.Retired > int64(batch) {
			t.Fatalf("batch retired %d rows, limit %d", res.Retired, batch)
		}
		if res.Done {
			return total, calls
		}
		if res.NextID <= after {
			t.Fatalf("cursor did not advance: %d -> %d", after, res.NextID)
		}
		after = res.NextID
	}
}

func TestRetireStale_RetiresOnlyEligibleRows(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	eligible := mustInsertProxy(t, repo, "10.7.0.1", 9001)
	seedScanState(t, driver, eligible, `fail_count = 12, first_seen_at = now() - interval '20 days'`)
	young := mustInsertProxy(t, repo, "10.7.0.2", 9001)
	seedScanState(t, driver, young, `fail_count = 12, first_seen_at = now() - interval '2 days'`)
	fewFailures := mustInsertProxy(t, repo, "10.7.0.3", 9001)
	seedScanState(t, driver, fewFailures, `fail_count = 4, first_seen_at = now() - interval '20 days'`)
	onceWorked := mustInsertProxy(t, repo, "10.7.0.4", 9001)
	seedScanState(t, driver, onceWorked,
		`fail_count = 40, last_alive_at = now() - interval '10 days', first_seen_at = now() - interval '60 days'`)

	total, _ := sweepAll(t, repo, retireRule, 100)
	if total != 1 {
		t.Fatalf("sweep retired %d rows, want 1", total)
	}
	for id, want := range map[int64]bool{eligible: true, young: false, fewFailures: false, onceWorked: false} {
		if got := mustGetProxy(t, repo, id).RetiredAt != nil; got != want {
			t.Errorf("proxy %d retired = %v, want %v", id, got, want)
		}
	}
}

func TestRetireStale_WorksInBatchesAndIsIdempotent(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, fail_count, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.7.' || (g / 250) || '.' || (g % 250), 9000, 11, 'test',
			now() - interval '30 days', now(), now()
		FROM generate_series(1, 25) g`)

	total, calls := sweepAll(t, repo, retireRule, 10)
	if total != 25 {
		t.Fatalf("first sweep retired %d rows, want 25", total)
	}
	if calls < 3 {
		t.Fatalf("sweep finished in %d calls, want it split across batches of 10", calls)
	}

	var firstTimes int64
	if err := scalar(t, driver, `SELECT count(DISTINCT retired_at) FROM proxies`, &firstTimes); err != nil {
		t.Fatal(err)
	}
	total, _ = sweepAll(t, repo, retireRule, 10)
	if total != 0 {
		t.Fatalf("second sweep retired %d rows, want 0", total)
	}
	var secondTimes int64
	if err := scalar(t, driver, `SELECT count(DISTINCT retired_at) FROM proxies`, &secondTimes); err != nil {
		t.Fatal(err)
	}
	if firstTimes != secondTimes {
		t.Fatalf("second sweep changed retirement times (%d -> %d distinct values)", firstTimes, secondTimes)
	}
}

func TestRetireStale_RejectsBadArguments(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	if _, err := repo.RetireStale(context.Background(), retireRule, 0, 0); err == nil {
		t.Error("batch size 0 accepted")
	}
	res, err := repo.RetireStale(context.Background(), contract.RetirementRule{}, 0, 10)
	if err != nil || !res.Done || res.Retired != 0 {
		t.Errorf("disabled rule = %+v, %v; want an immediate no-op", res, err)
	}
}

func TestCountByStatus_ReportsRetiredSeparately(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	healthy := mustInsertProxy(t, repo, "10.8.5.1", 9001)
	seedScanState(t, driver, healthy,
		`general_healthy = TRUE, status = 'active', last_verified_at = statement_timestamp()`)
	dead := mustInsertProxy(t, repo, "10.8.5.2", 9001)
	seedScanState(t, driver, dead, `status = 'dead'`)
	retired := mustInsertProxy(t, repo, "10.8.5.3", 9001)
	seedScanState(t, driver, retired, `status = 'dead', retired_at = statement_timestamp()`)

	counts, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	want := map[string]int64{"active": 1, "dead": 1, "retired": 1}
	if len(counts) != len(want) {
		t.Fatalf("CountByStatus = %v, want %v", counts, want)
	}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("CountByStatus[%q] = %d, want %d (all: %v)", k, counts[k], v, counts)
		}
	}
}

func endpoint(ip string, port int) contract.ProxyEndpoint {
	return contract.ProxyEndpoint{Protocol: "http", IP: ip, Port: port}
}

func TestMarkListed_StampsKnownEndpointsOnly(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	known := mustInsertProxy(t, repo, "10.8.0.1", 9001)
	other := mustInsertProxy(t, repo, "10.8.0.2", 9001)
	before := mustGetProxy(t, repo, known)

	res, err := repo.MarkListed(ctx, []contract.ProxyEndpoint{endpoint("10.8.0.1", 9001), endpoint("10.8.9.9", 9001)})
	if err != nil {
		t.Fatalf("MarkListed: %v", err)
	}
	if res.Marked != 1 || res.Revived != 0 {
		t.Fatalf("result = %+v, want 1 marked and 0 revived", res)
	}
	got := mustGetProxy(t, repo, known)
	if got.LastListedAt == nil {
		t.Fatal("known proxy not stamped")
	}
	if got.FailCount != before.FailCount || got.Status != before.Status ||
		got.ObservationVersion != before.ObservationVersion || got.GeneralHealthy != before.GeneralHealthy {
		t.Errorf("listing changed scan evidence: before %+v after %+v", before, got)
	}
	if mustGetProxy(t, repo, other).LastListedAt != nil {
		t.Error("an endpoint that was not listed got stamped")
	}
	if n, err := repo.Count(ctx); err != nil || n != 2 {
		t.Fatalf("Count = %d, %v; listing an unknown endpoint must not insert it", n, err)
	}
}

func TestMarkListed_RevivesRetiredProxy(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.8.1.1", 9001)
	seedScanState(t, driver, id, `retired_at = now() - interval '3 days', fail_count = 14`)

	res, err := repo.MarkListed(ctx, []contract.ProxyEndpoint{endpoint("10.8.1.1", 9001)})
	if err != nil {
		t.Fatalf("MarkListed: %v", err)
	}
	if res.Marked != 1 || res.Revived != 1 {
		t.Fatalf("result = %+v, want 1 marked and 1 revived", res)
	}
	if p := mustGetProxy(t, repo, id); p.RetiredAt != nil || p.LastListedAt == nil {
		t.Fatalf("retired_at = %v, last_listed_at = %v; want revived and stamped", p.RetiredAt, p.LastListedAt)
	}
	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 5)
	if err != nil || len(leases) != 1 || leases[0].Record.ID != id {
		t.Fatalf("revived proxy not claimable by the background scan: %v, %v", leaseIDs(leases), err)
	}
}

func TestMarkListed_AtMostOncePerHourPerRow(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.8.2.1", 9001)
	ep := []contract.ProxyEndpoint{endpoint("10.8.2.1", 9001)}

	if res, err := repo.MarkListed(ctx, ep); err != nil || res.Marked != 1 {
		t.Fatalf("first MarkListed = %+v, %v; want 1 marked", res, err)
	}
	first := mustGetProxy(t, repo, id).LastListedAt

	// Retired right after, then listed again within the hour: no write.
	seedScanState(t, driver, id, `retired_at = statement_timestamp()`)
	if res, err := repo.MarkListed(ctx, ep); err != nil || res.Marked != 0 || res.Revived != 0 {
		t.Fatalf("second MarkListed = %+v, %v; want a no-op within the hour", res, err)
	}
	p := mustGetProxy(t, repo, id)
	if !p.LastListedAt.Equal(*first) || p.RetiredAt == nil {
		t.Fatalf("row written within the hour: last_listed_at %v (was %v), retired_at %v", p.LastListedAt, first, p.RetiredAt)
	}

	// An hour later the listing counts again and revives the row.
	seedScanState(t, driver, id, `last_listed_at = now() - interval '61 minutes'`)
	if res, err := repo.MarkListed(ctx, ep); err != nil || res.Marked != 1 || res.Revived != 1 {
		t.Fatalf("third MarkListed = %+v, %v; want marked and revived", res, err)
	}
	if mustGetProxy(t, repo, id).RetiredAt != nil {
		t.Fatal("still retired after the hour elapsed")
	}
}

func TestMarkListed_BulkWithDuplicatesAndEmptyInput(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	if res, err := repo.MarkListed(ctx, nil); err != nil || res.Marked != 0 {
		t.Fatalf("empty MarkListed = %+v, %v", res, err)
	}

	const rows = 12000 // more than one statement's worth
	mustExec(t, driver, `INSERT INTO proxies (protocol, ip, port, source, first_seen_at, created_at, updated_at)
		SELECT 'http', '10.9.' || (g / 250) || '.' || (g % 250), 9000, 'test', now(), now(), now()
		FROM generate_series(1, $1::int) g`, rows)
	endpoints := make([]contract.ProxyEndpoint, 0, rows+2)
	for g := 1; g <= rows; g++ {
		endpoints = append(endpoints, endpoint(fmt.Sprintf("10.9.%d.%d", g/250, g%250), 9000))
	}
	endpoints = append(endpoints, endpoints[0], endpoints[1]) // duplicates in one list

	res, err := repo.MarkListed(ctx, endpoints)
	if err != nil {
		t.Fatalf("MarkListed: %v", err)
	}
	if res.Marked != rows {
		t.Fatalf("marked %d rows, want %d", res.Marked, rows)
	}
	var stamped int64
	if err := scalar(t, driver, `SELECT count(*) FROM proxies WHERE last_listed_at IS NOT NULL`, &stamped); err != nil || stamped != rows {
		t.Fatalf("stamped rows = %d, %v; want %d", stamped, err, rows)
	}
}

func TestMarkListed_DoesNotWaitForRowsBeingScanned(t *testing.T) {
	driver := newTestDriver(t)
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	busy := mustInsertProxy(t, repo, "10.8.3.1", 9001)
	idle := mustInsertProxy(t, repo, "10.8.3.2", 9001)

	db, err := driver.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test cleanup
	if _, err := tx.ExecContext(context.Background(), `SELECT 1 FROM proxies WHERE id = $1 FOR UPDATE`, busy); err != nil {
		t.Fatalf("lock row: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := repo.MarkListed(ctx, []contract.ProxyEndpoint{endpoint("10.8.3.1", 9001), endpoint("10.8.3.2", 9001)})
	if err != nil {
		t.Fatalf("MarkListed blocked or failed on a locked row: %v", err)
	}
	if res.Marked != 1 {
		t.Fatalf("marked %d rows, want only the unlocked one", res.Marked)
	}
	if mustGetProxy(t, repo, idle).LastListedAt == nil {
		t.Fatal("unlocked row not stamped")
	}
}

func TestDiscoveryCannotReinsertRetiredProxy(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.8.4.1", 9001)
	seedScanState(t, driver, id, `retired_at = statement_timestamp(), fail_count = 12`)

	newID, inserted, err := repo.InsertIfAbsent(ctx, contract.ProxyRecord{
		Protocol: "http", IP: "10.8.4.1", Port: 9001, Source: "other-provider",
	})
	if err != nil || inserted || newID != 0 {
		t.Fatalf("InsertIfAbsent = %d, %v, %v; want the retired tuple rejected", newID, inserted, err)
	}
	if exists, err := repo.Exists(ctx, "http", "10.8.4.1", 9001); err != nil || !exists {
		t.Fatalf("Exists = %v, %v; a retired proxy must stay in the table", exists, err)
	}
	if n, err := repo.Count(ctx); err != nil || n != 1 {
		t.Fatalf("Count = %d, %v; want the single retired row", n, err)
	}
	if p := mustGetProxy(t, repo, id); p.RetiredAt == nil || p.FailCount != 12 {
		t.Fatalf("retired row was modified by the insert attempt: %+v", p)
	}
}

func scalar(t *testing.T, driver contract.DatabaseDriver, query string, dest any) error {
	t.Helper()
	db, err := driver.Acquire(context.Background())
	if err != nil {
		return err
	}
	return db.QueryRowContext(context.Background(), query).Scan(dest)
}

func leaseIDs(leases []contract.ScanLease) []int64 {
	ids := make([]int64, 0, len(leases))
	for _, l := range leases {
		ids = append(ids, l.Record.ID)
	}
	return ids
}

func TestClaimScansBackground_ExcludesRetired(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	live := mustInsertProxy(t, repo, "10.5.0.1", 9001)
	retired := mustInsertProxy(t, repo, "10.5.0.2", 9002)
	seedScanState(t, driver, retired, `retired_at = statement_timestamp()`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 10)
	if err != nil {
		t.Fatalf("ClaimScans: %v", err)
	}
	if len(leases) != 1 || leases[0].Record.ID != live {
		t.Fatalf("background claim = %v, want only the live proxy %d", leaseIDs(leases), live)
	}
}
