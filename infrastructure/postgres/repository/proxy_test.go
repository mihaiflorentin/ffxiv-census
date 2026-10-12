package repository_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/infrastructure/postgres/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

func TestProxyScanMigration_AddsScanSchedulingColumns(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()

	rows, err := driver.FetchMany(ctx,
		`SELECT column_name, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'proxies'`)
	if err != nil {
		t.Fatalf("query proxy columns: %v", err)
	}
	defer rows.Close()
	got := map[string][2]string{}
	for rows.Next() {
		var name, nullable string
		var def sql.NullString
		if err := rows.Scan(&name, &nullable, &def); err != nil {
			t.Fatalf("scan column row: %v", err)
		}
		got[name] = [2]string{nullable, def.String}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}

	want := []string{
		"general_healthy", "last_completed_at", "last_verified_at", "next_attempt_at",
		"scan_not_before", "recovery_step", "observation_version", "scan_token",
		"scan_lease_until", "claimed_version", "destination_cooldown_until",
	}
	for _, col := range want {
		if _, ok := got[col]; !ok {
			t.Errorf("proxies.%s missing after migration", col)
		}
	}
	// Scheduled columns start empty; existing rows stay historically inactive.
	if c := got["general_healthy"]; c[0] != "NO" || c[1] != "false" {
		t.Errorf("general_healthy nullable=%q default=%q, want NOT NULL default false", c[0], c[1])
	}
	if c := got["recovery_step"]; c[0] != "NO" || c[1] != "0" {
		t.Errorf("recovery_step nullable=%q default=%q, want NOT NULL default 0", c[0], c[1])
	}
	if c := got["observation_version"]; c[0] != "NO" || c[1] != "0" {
		t.Errorf("observation_version nullable=%q default=%q, want NOT NULL default 0", c[0], c[1])
	}

	idxRows, err := driver.FetchMany(ctx,
		`SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'proxies'`)
	if err != nil {
		t.Fatalf("query proxy indexes: %v", err)
	}
	defer idxRows.Close()
	indexes := map[string]string{}
	for idxRows.Next() {
		var name, def string
		if err := idxRows.Scan(&name, &def); err != nil {
			t.Fatalf("scan index row: %v", err)
		}
		indexes[name] = def
	}
	if err := idxRows.Err(); err != nil {
		t.Fatalf("iterate indexes: %v", err)
	}

	wantIndexes := map[string]string{
		"idx_proxies_verification_due":     "WHERE general_healthy",
		"idx_proxies_recovery_due":         "WHERE ((NOT general_healthy) AND (last_verified_at IS NOT NULL))",
		"idx_proxies_background_completed": "WHERE (NOT general_healthy)",
		"idx_proxies_fresh_verified":       "WHERE general_healthy",
	}
	for name, predicate := range wantIndexes {
		def, ok := indexes[name]
		if !ok {
			t.Errorf("index %s missing after migration", name)
			continue
		}
		if !strings.Contains(def, predicate) {
			t.Errorf("index %s predicate mismatch:\n got: %s\nwant substring: %s", name, def, predicate)
		}
		if strings.Contains(strings.ToLower(def), "now()") {
			t.Errorf("index %s uses non-immutable now() in predicate: %s", name, def)
		}
	}
}

func TestClaimScansBackground_ConcurrentDisjointClaims(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repoA := repository.NewProxyRepository(driver, testScanPolicy())
	repoB := repository.NewProxyRepository(driver, testScanPolicy())

	const total = 12
	for i := range total {
		mustInsertProxy(t, repoA, fmt.Sprintf("10.1.0.%d", i+1), 9000+i)
	}

	var mu sync.Mutex
	claimed := map[int64]int{}
	tokenRows := map[string]int{}
	var wg sync.WaitGroup
	for _, store := range []contract.ProxyScanStore{repoA, repoB} {
		wg.Add(1)
		go func(store contract.ProxyScanStore) {
			defer wg.Done()
			for {
				leases, err := store.ClaimScans(ctx, contract.ScanBackground, 2)
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
					tokenRows[l.Token]++
					if l.Token == "" {
						t.Errorf("lease for proxy %d has empty token", l.Record.ID)
					}
					if l.Version != l.Record.ObservationVersion {
						t.Errorf("lease version %d does not match claimed observation version %d",
							l.Version, l.Record.ObservationVersion)
					}
					if l.Record.ScanToken == nil || *l.Record.ScanToken != l.Token {
						t.Errorf("lease record for proxy %d carries wrong scan token", l.Record.ID)
					}
					if l.Record.ScanLeaseUntil == nil {
						t.Errorf("lease for proxy %d has no expiry", l.Record.ID)
					}
				}
				mu.Unlock()
			}
		}(store)
	}
	wg.Wait()

	if len(claimed) != total {
		t.Fatalf("claimed %d distinct proxies, want %d", len(claimed), total)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("proxy %d claimed %d times", id, n)
		}
	}
	// Two rows per batch: every batch must carry its own fresh token.
	if len(tokenRows) != total/2 {
		t.Errorf("got %d distinct batch tokens, want %d (tokens must never repeat across batches)",
			len(tokenRows), total/2)
	}
	for token, rows := range tokenRows {
		if rows != 2 {
			t.Errorf("token %s covers %d rows, want 2 per batch", token, rows)
		}
	}
}

func TestClaimScans_RecoveryAndBackgroundOverlap(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repoA := repository.NewProxyRepository(driver, testScanPolicy())
	repoB := repository.NewProxyRepository(driver, testScanPolicy())

	// Both rows are unhealthy and recently verified (inside the recovery
	// horizon). idDue is past its recovery deadline; idFuture's deadline is
	// still hours away.
	idDue := mustInsertProxy(t, repoA, "10.2.0.1", 9001)
	seedScanState(t, driver, idDue, `
		general_healthy = false,
		last_verified_at = statement_timestamp() - interval '10 minutes',
		next_attempt_at = statement_timestamp() - interval '1 second'`)
	idFuture := mustInsertProxy(t, repoA, "10.2.0.2", 9002)
	seedScanState(t, driver, idFuture, `
		general_healthy = false,
		last_verified_at = statement_timestamp() - interval '10 minutes',
		next_attempt_at = statement_timestamp() + interval '10 minutes'`)

	recovery, err := repoA.ClaimScans(ctx, contract.ScanRecovery, 10)
	if err != nil {
		t.Fatalf("recovery claim: %v", err)
	}
	if len(recovery) != 1 || recovery[0].Record.ID != idDue {
		t.Fatalf("recovery claim returned %+v, want only proxy %d (future deadline excluded)", recovery, idDue)
	}

	background, err := repoB.ClaimScans(ctx, contract.ScanBackground, 10)
	if err != nil {
		t.Fatalf("background claim: %v", err)
	}
	if len(background) != 1 || background[0].Record.ID != idFuture {
		t.Fatalf("background claim returned %+v, want only proxy %d (leased rows excluded, deadline ignored)",
			background, idFuture)
	}
	for _, l := range recovery {
		for _, b := range background {
			if l.Record.ID == b.Record.ID {
				t.Errorf("proxy %d claimed by both recovery and background", l.Record.ID)
			}
		}
	}
}

func TestClaimScansBackground_OrdersByCompletionNullsFirst(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	idA := mustInsertProxy(t, repo, "10.3.0.1", 9001)
	seedScanState(t, driver, idA, `general_healthy = false, last_completed_at = statement_timestamp() - interval '1 hour'`)
	idB := mustInsertProxy(t, repo, "10.3.0.2", 9002)
	seedScanState(t, driver, idB, `general_healthy = false, last_completed_at = statement_timestamp() - interval '2 hours'`)
	idC := mustInsertProxy(t, repo, "10.3.0.3", 9003)
	seedScanState(t, driver, idC, `general_healthy = false`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 3)
	if err != nil {
		t.Fatalf("background claim: %v", err)
	}
	want := []int64{idC, idB, idA}
	if len(leases) != len(want) {
		t.Fatalf("claimed %d leases, want %d", len(leases), len(want))
	}
	for i, l := range leases {
		if l.Record.ID != want[i] {
			t.Fatalf("claim order[%d] = proxy %d, want %d (last_completed NULLS FIRST, then id)",
				i, l.Record.ID, want[i])
		}
	}
}

func TestClaimScans_LeaseExpiryAndNewerTokenProtection(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.4.0.1", 9001)
	seedScanState(t, driver, id, `general_healthy = false`)

	first, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %v, %d leases", err, len(first))
	}

	// Expire the lease with a SQL-relative timestamp, then claim again: the
	// same row must come back under a different token.
	seedScanState(t, driver, id, `scan_lease_until = statement_timestamp() - interval '1 second'`)
	second, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("claim after expiry: %v, %d leases", err, len(second))
	}
	if second[0].Record.ID != id {
		t.Fatalf("re-claim returned proxy %d, want %d", second[0].Record.ID, id)
	}
	if second[0].Token == first[0].Token {
		t.Fatal("re-claim reused the expired batch token")
	}

	// The superseded holder can neither release nor complete the newer lease.
	if err := repo.ReleaseScan(ctx, first[0]); err != nil {
		t.Fatalf("superseded release: %v", err)
	}
	p := mustGetProxy(t, repo, id)
	if p.ScanToken == nil || *p.ScanToken != second[0].Token {
		t.Fatal("superseded release cleared the newer lease token")
	}
	accepted, err := repo.CompleteScan(ctx, first[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil {
		t.Fatalf("superseded completion: %v", err)
	}
	if accepted {
		t.Fatal("superseded completion was accepted")
	}
	accepted, err = repo.CompleteScan(ctx, second[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("current completion: accepted=%v err=%v", accepted, err)
	}
}

func TestCompleteScan_ReplayRejected(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.5.0.1", 9001)
	seedScanState(t, driver, id, `general_healthy = false`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("claim: %v, %d leases", err, len(leases))
	}
	accepted, err := repo.CompleteScan(ctx, leases[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("first completion: accepted=%v err=%v", accepted, err)
	}
	p := mustGetProxy(t, repo, id)
	completed := *p.LastCompletedAt

	accepted, err = repo.CompleteScan(ctx, leases[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if accepted {
		t.Fatal("replayed completion was accepted")
	}
	p = mustGetProxy(t, repo, id)
	if !p.LastCompletedAt.Equal(completed) {
		t.Error("replay advanced completion time")
	}
	if p.ObservationVersion != 1 {
		t.Errorf("replay bumped observation version to %d, want 1", p.ObservationVersion)
	}
}

func TestClaimScans_ValidatesQueueAndLimit(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	if _, err := repo.ClaimScans(ctx, contract.ScanBackground, 0); err == nil {
		t.Error("limit 0 must be an error, never unlimited")
	}
	if _, err := repo.ClaimScans(ctx, contract.ScanBackground, -3); err == nil {
		t.Error("negative limit must be an error")
	}
	if _, err := repo.ClaimScans(ctx, contract.ScanQueue(99), 5); err == nil {
		t.Error("unknown queue must be an error")
	}
}

// healthyViaCompletion drives a proxy to verified-healthy state through the
// claim/completion path only — never by writing last_verified_at directly.
func healthyViaCompletion(t *testing.T, driver contract.DatabaseDriver, repo contract.ProxyScanStore, id int64) {
	t.Helper()
	seedScanState(t, driver, id, `general_healthy = false`)
	leases, err := repo.ClaimScans(context.Background(), contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("background claim for proxy %d: %v, %d leases", id, err, len(leases))
	}
	accepted, err := repo.CompleteScan(context.Background(), leases[0],
		scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("completion for proxy %d: accepted=%v err=%v", id, accepted, err)
	}
}

func TestCompleteScanRejectsSupersededVersion(t *testing.T) {
	ctx := context.Background()
	driver := newTestDriver(t)
	policy := contract.ProxyScanPolicy{
		VerifyInterval: time.Minute, FreshnessTTL: 2 * time.Minute,
		RecoveryBase: time.Minute, RecoveryCap: 15 * time.Minute,
		RecoveryHorizon: time.Hour, LeaseDuration: 30 * time.Second,
		MinRepeatInterval: time.Second, InconclusiveRetry: time.Minute,
		DeadAfter: 48 * time.Hour, FailThreshold: 5,
	}
	repo := repository.NewProxyRepository(driver, policy)
	id, _, err := repo.InsertIfAbsent(ctx, contract.ProxyRecord{
		Protocol: "http", IP: "192.0.2.1", Port: 8080, Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("claim: %v, %v", leases, err)
	}
	_, err = driver.Execute(ctx,
		`UPDATE proxies SET observation_version=observation_version+1 WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := repo.CompleteScan(ctx, leases[0], contract.ScanUpdate{
		Outcome: contract.ScanSuccess, LatencyMS: 42,
		NextDelay: time.Minute, RepeatDelay: time.Second,
		FreshnessTTL: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted {
		t.Fatal("superseded scan changed health")
	}
	active, err := repo.ListActive(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatal("stale completion advertised a usable proxy")
	}
}

func TestCompleteScan_SuccessAndFailureTransitions(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.6.0.1", 9001)
	seedScanState(t, driver, id, `general_healthy = false`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("claim: %v, %d leases", err, len(leases))
	}
	accepted, err := repo.CompleteScan(ctx, leases[0], scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("success completion: accepted=%v err=%v", accepted, err)
	}
	p := mustGetProxy(t, repo, id)
	if !p.GeneralHealthy || p.Status != contract.ProxyStatusActive {
		t.Errorf("success state: healthy=%v status=%s, want healthy/active", p.GeneralHealthy, p.Status)
	}
	if p.FailCount != 0 || p.RecoveryStep != 0 || p.ObservationVersion != 1 {
		t.Errorf("success counters: fail=%d step=%d version=%d, want 0/0/1",
			p.FailCount, p.RecoveryStep, p.ObservationVersion)
	}
	if p.LatencyMS == nil || *p.LatencyMS != 42 {
		t.Errorf("success latency %v, want 42", p.LatencyMS)
	}
	if p.LastAliveAt == nil || p.LastVerifiedAt == nil || p.LastCompletedAt == nil {
		t.Error("success must stamp last alive, verified and completed times")
	}
	if p.ScanToken != nil || p.ScanLeaseUntil != nil || p.ClaimedVersion != nil {
		t.Error("accepted completion must clear the scan lease fields")
	}
	if p.NextAttemptAt == nil || p.NextAttemptAt.Before(time.Now().UTC()) {
		t.Errorf("success next attempt %v, want a future verification deadline", p.NextAttemptAt)
	}
	if p.ScanNotBefore == nil {
		t.Error("success must set the repeat floor")
	}
	active, err := repo.ListActive(ctx, 10)
	if err != nil || len(active) != 1 || active[0].ID != id {
		t.Fatalf("ListActive after success: %v, %v", active, err)
	}

	// Failure preserves the last success evidence and schedules recovery.
	prevAlive := *p.LastAliveAt
	prevVerified := *p.LastVerifiedAt
	mustExec(t, driver,
		`UPDATE proxies SET next_attempt_at = statement_timestamp() - interval '1 second', scan_not_before = statement_timestamp() - interval '1 second' WHERE id = $1`, id)
	vLeases, err := repo.ClaimScans(ctx, contract.ScanVerification, 1)
	if err != nil || len(vLeases) != 1 {
		t.Fatalf("verification claim: %v, %d leases", err, len(vLeases))
	}
	failure := scanUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	failure.RecoveryStep = 2
	accepted, err = repo.CompleteScan(ctx, vLeases[0], failure)
	if err != nil || !accepted {
		t.Fatalf("failure completion: accepted=%v err=%v", accepted, err)
	}
	p = mustGetProxy(t, repo, id)
	if p.GeneralHealthy || p.Status != contract.ProxyStatusInactive {
		t.Errorf("failure state: healthy=%v status=%s, want unhealthy/inactive", p.GeneralHealthy, p.Status)
	}
	if p.FailCount != 1 || p.RecoveryStep != 2 || p.ObservationVersion != 2 {
		t.Errorf("failure counters: fail=%d step=%d version=%d, want 1/2/2",
			p.FailCount, p.RecoveryStep, p.ObservationVersion)
	}
	if p.LastAliveAt == nil || !p.LastAliveAt.Equal(prevAlive) {
		t.Error("failure must preserve last success alive time")
	}
	if p.LastVerifiedAt == nil || !p.LastVerifiedAt.Equal(prevVerified) {
		t.Error("failure must preserve last verification time")
	}
	if p.LatencyMS == nil || *p.LatencyMS != 42 {
		t.Error("failure must preserve last measured latency")
	}
	if active, err := repo.ListActive(ctx, 10); err != nil || len(active) != 0 {
		t.Fatalf("ListActive after failure: %v, %v", active, err)
	}

	// Crossing the fail threshold marks the row dead.
	mustExec(t, driver,
		`UPDATE proxies SET next_attempt_at = statement_timestamp() - interval '1 second', scan_not_before = statement_timestamp() - interval '1 second' WHERE id = $1`, id)
	vLeases, err = repo.ClaimScans(ctx, contract.ScanRecovery, 1)
	if err != nil || len(vLeases) != 1 {
		t.Fatalf("verification claim: %v, %d leases", err, len(vLeases))
	}
	threshold := scanUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	threshold.FailThreshold = 2
	accepted, err = repo.CompleteScan(ctx, vLeases[0], threshold)
	if err != nil || !accepted {
		t.Fatalf("threshold completion: accepted=%v err=%v", accepted, err)
	}
	p = mustGetProxy(t, repo, id)
	if p.Status != contract.ProxyStatusDead || p.FailCount != 2 {
		t.Errorf("threshold state: status=%s fail=%d, want dead/2", p.Status, p.FailCount)
	}

	// Ageing past DeadAfter marks the row dead even below the threshold.
	id2 := mustInsertProxy(t, repo, "10.6.0.2", 9002)
	healthyViaCompletion(t, driver, repo, id2)
	seedScanState(t, driver, id2, `
		last_alive_at = statement_timestamp() - interval '49 hours',
		next_attempt_at = statement_timestamp() - interval '1 second',
		scan_not_before = statement_timestamp() - interval '1 second'`)
	vLeases2, err := repo.ClaimScans(ctx, contract.ScanVerification, 1)
	if err != nil || len(vLeases2) != 1 || vLeases2[0].Record.ID != id2 {
		t.Fatalf("verification claim for id2: %v, %d leases", err, len(vLeases2))
	}
	aged := scanUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	accepted, err = repo.CompleteScan(ctx, vLeases2[0], aged)
	if err != nil || !accepted {
		t.Fatalf("aged completion: accepted=%v err=%v", accepted, err)
	}
	p = mustGetProxy(t, repo, id2)
	if p.Status != contract.ProxyStatusDead {
		t.Errorf("aged state: status=%s, want dead (last alive beyond DeadAfter)", p.Status)
	}
}

func TestCompleteScan_InconclusiveAfterSuccess_StaysVerification(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.7.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)
	p := mustGetProxy(t, repo, id)
	version := p.ObservationVersion
	verified := *p.LastVerifiedAt

	mustExec(t, driver,
		`UPDATE proxies SET next_attempt_at = statement_timestamp() - interval '1 second', scan_not_before = statement_timestamp() - interval '1 second' WHERE id = $1`, id)
	leases, err := repo.ClaimScans(ctx, contract.ScanVerification, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("verification claim: %v, %d leases", err, len(leases))
	}
	accepted, err := repo.CompleteScan(ctx, leases[0],
		scanUpdate(contract.ScanInconclusive, time.Minute, time.Minute))
	if err != nil || !accepted {
		t.Fatalf("inconclusive completion: accepted=%v err=%v", accepted, err)
	}
	p = mustGetProxy(t, repo, id)
	if !p.GeneralHealthy || p.Status != contract.ProxyStatusActive {
		t.Error("inconclusive must preserve the healthy flag and status")
	}
	if p.FailCount != 0 || p.ObservationVersion != version || p.RecoveryStep != 0 {
		t.Errorf("inconclusive preserved counters: fail=%d version=%d step=%d, want 0/%d/0",
			p.FailCount, p.ObservationVersion, p.RecoveryStep, version)
	}
	if !p.LastVerifiedAt.Equal(verified) {
		t.Error("inconclusive must preserve the last verification time")
	}
	if p.LastCompletedAt == nil {
		t.Error("inconclusive is a real attempt and must advance completed time")
	}

	// Inside the repeat window the row is not claimable yet.
	again, err := repo.ClaimScans(ctx, contract.ScanVerification, 5)
	if err != nil {
		t.Fatalf("verification claim after inconclusive: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("inconclusive row claimable inside repeat window: %d leases", len(again))
	}
	// Once the window passes it stays in the verification queue.
	seedScanState(t, driver, id, `
		next_attempt_at = statement_timestamp() - interval '1 second',
		scan_not_before = statement_timestamp() - interval '1 second'`)
	again, err = repo.ClaimScans(ctx, contract.ScanVerification, 5)
	if err != nil || len(again) != 1 || again[0].Record.ID != id {
		t.Fatalf("verification claim after window: %v, %d leases", err, len(again))
	}
}

func TestFreshReads_ExactFreshnessBoundaryExcluded(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	// Policy freshness TTL is 2 minutes; a row verified exactly TTL ago is
	// stale because the predicate is strict.
	idEdge := mustInsertProxy(t, repo, "10.9.0.1", 9001)
	seedScanState(t, driver, idEdge, `
		general_healthy = true, status = 'active',
		last_verified_at = statement_timestamp() - interval '2 minutes',
		last_alive_at = statement_timestamp() - interval '2 minutes'`)
	idIn := mustInsertProxy(t, repo, "10.9.0.2", 9002)
	seedScanState(t, driver, idIn, `
		general_healthy = true, status = 'active',
		last_verified_at = statement_timestamp() - interval '119 seconds',
		last_alive_at = statement_timestamp() - interval '119 seconds'`)

	active, err := repo.ListActive(ctx, 10)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(active) != 1 || active[0].ID != idIn {
		t.Fatalf("ListActive returned %+v, want only the fresh proxy %d", active, idIn)
	}

	counts, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts["active"] != 1 || counts["stale"] != 1 {
		t.Fatalf("counts active=%d stale=%d, want 1/1 (boundary row historically active maps to stale)",
			counts["active"], counts["stale"])
	}

	claimed, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if claimed == nil || claimed.ID != idIn {
		t.Fatalf("ClaimProxy returned %v, want the fresh proxy %d", claimed, idIn)
	}
	again, err := repo.ClaimProxy(ctx, "owner2", time.Minute)
	if err != nil {
		t.Fatalf("ClaimProxy second: %v", err)
	}
	if again != nil {
		t.Fatalf("ClaimProxy returned boundary proxy %d, want nil", again.ID)
	}
}

func TestClaimScansBackground_IgnoresRecoveryDeadlineRespectsCommonCooldown(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	// Background ignores a future recovery deadline...
	idDeadline := mustInsertProxy(t, repo, "10.10.0.1", 9001)
	seedScanState(t, driver, idDeadline, `
		general_healthy = false,
		next_attempt_at = statement_timestamp() + interval '1 hour'`)
	// ...but respects the common minimum-repeat cooldown.
	idCooldown := mustInsertProxy(t, repo, "10.10.0.2", 9002)
	seedScanState(t, driver, idCooldown, `
		general_healthy = false,
		scan_not_before = statement_timestamp() + interval '1 hour'`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 10)
	if err != nil {
		t.Fatalf("background claim: %v", err)
	}
	if len(leases) != 1 || leases[0].Record.ID != idDeadline {
		t.Fatalf("background claim returned %+v, want only proxy %d", leases, idDeadline)
	}
}

func TestLegacyActiveRow_ExcludedFromFreshReads(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := seedLegacyActiveProxy(t, driver, "10.11.0.1")

	active, err := repo.ListActive(ctx, 10)
	if err != nil || len(active) != 0 {
		t.Fatalf("ListActive on legacy row: %v, %v", active, err)
	}
	counts, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts["active"] != 0 || counts["stale"] != 1 {
		t.Fatalf("counts active=%d stale=%d, want 0/1", counts["active"], counts["stale"])
	}
	claimed, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || claimed != nil {
		t.Fatalf("ClaimProxy on legacy row: %v, %v", claimed, err)
	}
	// Never-verified rows sit outside the recovery horizon...
	recovery, err := repo.ClaimScans(ctx, contract.ScanRecovery, 10)
	if err != nil || len(recovery) != 0 {
		t.Fatalf("recovery claim on legacy row: %v, %d leases", err, len(recovery))
	}
	// ...but remain background work.
	background, err := repo.ClaimScans(ctx, contract.ScanBackground, 10)
	if err != nil || len(background) != 1 || background[0].Record.ID != id {
		t.Fatalf("background claim on legacy row: %v, %d leases", err, len(background))
	}
}

func TestDestinationCooldown_DoesNotReduceActiveCount(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.12.0.1", 9001)
	seedScanState(t, driver, id, `
		general_healthy = true, status = 'active',
		last_verified_at = statement_timestamp() - interval '30 seconds',
		last_alive_at = statement_timestamp() - interval '30 seconds',
		destination_cooldown_until = statement_timestamp() + interval '1 hour'`)

	counts, err := repo.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts["active"] != 1 {
		t.Fatalf("active count %d, want 1 (destination cooldown is not general evidence)", counts["active"])
	}
	active, err := repo.ListActive(ctx, 10)
	if err != nil || len(active) != 1 {
		t.Fatalf("ListActive: %v, %v", active, err)
	}
	claimed, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if claimed != nil {
		t.Fatalf("ClaimProxy returned cooled-down proxy %d, want nil", claimed.ID)
	}
}

func TestRefreshConsumerLock_ValidatesAndExtends(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.13.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)
	rec, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	got, err := repo.RefreshConsumerLock(ctx, id, "owner", time.Minute)
	if err != nil {
		t.Fatalf("RefreshConsumerLock: %v", err)
	}
	if got == nil {
		t.Fatal("RefreshConsumerLock rejected a valid owned lock")
	}
	if got.ID != id || got.ObservationVersion != rec.ObservationVersion {
		t.Errorf("refresh returned id=%d version=%d, want id=%d version=%d",
			got.ID, got.ObservationVersion, id, rec.ObservationVersion)
	}
	if got.LockedBy == nil || *got.LockedBy != "owner" {
		t.Error("refresh must keep ownership")
	}

	// Owner isolation: another owner is rejected and cannot disturb the lock.
	got, err = repo.RefreshConsumerLock(ctx, id, "other", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("RefreshConsumerLock other owner: %v, %v", got, err)
	}

	// Destination cooldown blocks the consumer path.
	seedScanState(t, driver, id, `destination_cooldown_until = statement_timestamp() + interval '1 hour'`)
	got, err = repo.RefreshConsumerLock(ctx, id, "owner", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("RefreshConsumerLock under cooldown: %v, %v", got, err)
	}

	// Stale health blocks the consumer path.
	seedScanState(t, driver, id, `destination_cooldown_until = NULL, general_healthy = false`)
	got, err = repo.RefreshConsumerLock(ctx, id, "owner", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("RefreshConsumerLock on stale row: %v, %v", got, err)
	}

	// Expired locks block refresh.
	seedScanState(t, driver, id, `
		general_healthy = true,
		last_verified_at = statement_timestamp() - interval '30 seconds',
		locked_at = statement_timestamp() - interval '10 minutes'`)
	got, err = repo.RefreshConsumerLock(ctx, id, "owner", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("RefreshConsumerLock with expired lock: %v, %v", got, err)
	}

	// A missing row is not an error.
	got, err = repo.RefreshConsumerLock(ctx, 999999, "owner", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("RefreshConsumerLock missing row: %v, %v", got, err)
	}
}

func TestRecordConsumerFailure_FencesScannerSuccess(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.14.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)
	rec, err := repo.ClaimProxy(ctx, "consumer", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	// A scanner claims the row for verification at the same version.
	mustExec(t, driver,
		`UPDATE proxies SET next_attempt_at = statement_timestamp() - interval '1 second', scan_not_before = statement_timestamp() - interval '1 second' WHERE id = $1`, id)
	vLeases, err := repo.ClaimScans(ctx, contract.ScanVerification, 1)
	if err != nil || len(vLeases) != 1 {
		t.Fatalf("verification claim: %v, %d leases", err, len(vLeases))
	}

	// The consumer records a conclusive failure first: it advances the
	// version and invalidates the outstanding scan lease.
	failure := scanUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	failure.RecoveryStep = 1
	accepted, err := repo.RecordConsumerFailure(ctx, *rec, "consumer", time.Minute, failure)
	if err != nil || !accepted {
		t.Fatalf("RecordConsumerFailure: accepted=%v err=%v", accepted, err)
	}
	p := mustGetProxy(t, repo, id)
	if p.GeneralHealthy || p.FailCount != 1 || p.ObservationVersion != 2 || p.RecoveryStep != 1 {
		t.Errorf("consumer failure state: healthy=%v fail=%d version=%d step=%d, want unhealthy/1/2/1",
			p.GeneralHealthy, p.FailCount, p.ObservationVersion, p.RecoveryStep)
	}
	if p.LastCompletedAt == nil {
		t.Error("consumer failure must advance completed time")
	}

	// The scanner's superseded lease must now be rejected.
	accepted, err = repo.CompleteScan(ctx, vLeases[0],
		scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil {
		t.Fatalf("fenced completion: %v", err)
	}
	if accepted {
		t.Fatal("fenced scanner completion was accepted")
	}
	p = mustGetProxy(t, repo, id)
	if p.GeneralHealthy || p.ObservationVersion != 2 {
		t.Error("fenced completion changed general evidence")
	}
}

func TestRecordConsumerFailure_StaleConsumerRejected(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.15.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)
	rec, err := repo.ClaimProxy(ctx, "consumer", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	// The scanner completes a verification success first.
	mustExec(t, driver,
		`UPDATE proxies SET next_attempt_at = statement_timestamp() - interval '1 second', scan_not_before = statement_timestamp() - interval '1 second' WHERE id = $1`, id)
	vLeases, err := repo.ClaimScans(ctx, contract.ScanVerification, 1)
	if err != nil || len(vLeases) != 1 {
		t.Fatalf("verification claim: %v, %d leases", err, len(vLeases))
	}
	accepted, err := repo.CompleteScan(ctx, vLeases[0],
		scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("scanner success: accepted=%v err=%v", accepted, err)
	}
	prevAlive := *mustGetProxy(t, repo, id).LastAliveAt

	// The consumer's captured record is now stale and must be rejected.
	accepted, err = repo.RecordConsumerFailure(ctx, *rec, "consumer", time.Minute,
		scanUpdate(contract.ScanFailure, 30*time.Second, time.Second))
	if err != nil {
		t.Fatalf("stale consumer failure: %v", err)
	}
	if accepted {
		t.Fatal("stale consumer failure was accepted")
	}
	p := mustGetProxy(t, repo, id)
	if !p.GeneralHealthy || p.FailCount != 0 || p.ObservationVersion != 2 {
		t.Errorf("stale consumer failure changed evidence: healthy=%v fail=%d version=%d",
			p.GeneralHealthy, p.FailCount, p.ObservationVersion)
	}
	if p.LastAliveAt == nil || !p.LastAliveAt.Equal(prevAlive) {
		t.Error("stale consumer failure must not touch last-alive evidence")
	}
	if p.LockedBy != nil {
		t.Error("rejected consumer must have its own valid lock released")
	}
}

func TestReleaseScan_ShutdownReleaseAndOwnerIsolation(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())
	id := mustInsertProxy(t, repo, "10.16.0.1", 9001)
	seedScanState(t, driver, id, `general_healthy = false`)

	leases, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(leases) != 1 {
		t.Fatalf("claim: %v, %d leases", err, len(leases))
	}
	if err := repo.ReleaseScan(ctx, leases[0]); err != nil {
		t.Fatalf("ReleaseScan: %v", err)
	}
	p := mustGetProxy(t, repo, id)
	if p.ScanToken != nil || p.ScanLeaseUntil != nil || p.ClaimedVersion != nil {
		t.Fatal("release did not clear the scan lease")
	}
	if p.GeneralHealthy {
		t.Error("release must not touch general evidence")
	}

	again, err := repo.ClaimScans(ctx, contract.ScanBackground, 1)
	if err != nil || len(again) != 1 || again[0].Record.ID != id {
		t.Fatalf("re-claim after release: %v, %d leases", err, len(again))
	}
	accepted, err := repo.CompleteScan(ctx, again[0],
		scanUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("completion after release: accepted=%v err=%v", accepted, err)
	}

	rec, err := repo.ClaimProxy(ctx, "owner-a", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	// Owner isolation: owner-b can neither refresh nor fail owner-a's lock.
	got, err := repo.RefreshConsumerLock(ctx, id, "owner-b", time.Minute)
	if err != nil || got != nil {
		t.Fatalf("owner-b refresh: %v, %v", got, err)
	}
	okB, err := repo.RecordConsumerFailure(ctx, *rec, "owner-b", time.Minute,
		scanUpdate(contract.ScanFailure, 30*time.Second, time.Second))
	if err != nil || okB {
		t.Fatalf("owner-b failure: accepted=%v err=%v", okB, err)
	}
	p = mustGetProxy(t, repo, id)
	if p.LockedBy == nil || *p.LockedBy != "owner-a" {
		t.Error("owner-b operations must not disturb owner-a's lock")
	}
	if !p.GeneralHealthy || p.ObservationVersion != 1 {
		t.Error("owner-b failure must not change evidence")
	}
	got, err = repo.RefreshConsumerLock(ctx, id, "owner-a", time.Minute)
	if err != nil || got == nil {
		t.Fatalf("owner-a refresh: %v, %v", got, err)
	}
}

func TestCooldownDestination_ExtendsAndReleasesOwnedLock(t *testing.T) {
	driver := newTestDriver(t)
	ctx := context.Background()
	repo := repository.NewProxyRepository(driver, testScanPolicy())

	// GREATEST semantics: an existing longer cooldown is not shortened.
	id := mustInsertProxy(t, repo, "10.17.0.1", 9001)
	healthyViaCompletion(t, driver, repo, id)
	rec, err := repo.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}
	seedScanState(t, driver, id, `destination_cooldown_until = statement_timestamp() + interval '2 hours'`)
	version := rec.ObservationVersion
	if err := repo.CooldownDestination(ctx, id, "owner", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination: %v", err)
	}
	p := mustGetProxy(t, repo, id)
	if p.DestinationCooldownUntil == nil || time.Until(*p.DestinationCooldownUntil) < 90*time.Minute {
		t.Errorf("cooldown %v shortened; GREATEST must keep the longer existing value", p.DestinationCooldownUntil)
	}
	if p.LockedBy != nil {
		t.Error("cooldown must release the owned consumer lock")
	}
	if !p.GeneralHealthy || p.ObservationVersion != version {
		t.Error("cooldown must not touch general evidence or scan version")
	}

	// Without an existing cooldown the delay sets it from database time.
	id2 := mustInsertProxy(t, repo, "10.17.0.2", 9002)
	healthyViaCompletion(t, driver, repo, id2)
	if _, err := repo.ClaimProxy(ctx, "owner2", time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if err := repo.CooldownDestination(ctx, id2, "owner2", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination: %v", err)
	}
	p2 := mustGetProxy(t, repo, id2)
	if p2.DestinationCooldownUntil == nil ||
		time.Until(*p2.DestinationCooldownUntil) < 55*time.Minute ||
		time.Until(*p2.DestinationCooldownUntil) > 65*time.Minute {
		t.Errorf("cooldown %v, want approximately now+1h", p2.DestinationCooldownUntil)
	}

	// Owner isolation: another owner can neither set cooldown nor release.
	id3 := mustInsertProxy(t, repo, "10.17.0.3", 9003)
	healthyViaCompletion(t, driver, repo, id3)
	if _, err := repo.ClaimProxy(ctx, "owner3", time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if err := repo.CooldownDestination(ctx, id3, "not-owner", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination wrong owner: %v", err)
	}
	p3 := mustGetProxy(t, repo, id3)
	if p3.DestinationCooldownUntil != nil {
		t.Error("wrong owner must not set a cooldown")
	}
	if p3.LockedBy == nil || *p3.LockedBy != "owner3" {
		t.Error("wrong owner must not release the lock")
	}
}
