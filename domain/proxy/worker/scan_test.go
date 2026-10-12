package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The scenario tests drive the real dispatcher end to end: gated checks use
// channel barriers instead of sleeps, every completion is observed through
// the store's CompleteScan path, and failure bounds exist only to fail a
// stuck test, never as correctness evidence.

func testLogger() contract.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testPolicy() contract.ProxyScanPolicy {
	return contract.ProxyScanPolicy{
		VerifyInterval:    500 * time.Millisecond,
		FreshnessTTL:      2 * time.Minute,
		RecoveryBase:      time.Millisecond,
		RecoveryCap:       5 * time.Millisecond,
		RecoveryHorizon:   time.Hour,
		LeaseDuration:     2 * time.Second,
		MinRepeatInterval: time.Millisecond,
		InconclusiveRetry: time.Millisecond,
		DeadAfter:         48 * time.Hour,
		FailThreshold:     5,
	}
}

// shortLeasePolicy keeps the lease barely longer than the claim timeout so
// tests that rely on lease-expiry recovery observe it within milliseconds.
func shortLeasePolicy() contract.ProxyScanPolicy {
	p := testPolicy()
	p.LeaseDuration = 200 * time.Millisecond
	return p
}

func seedRecord(t *testing.T, repo *repository.FakeProxyRepository, ip string, rec contract.ProxyRecord) int64 {
	t.Helper()
	rec.Protocol = "http"
	rec.IP = ip
	rec.Port = 8080
	rec.Source = "scan-test"
	id, inserted, err := repo.InsertIfAbsent(context.Background(), rec)
	if err != nil {
		t.Fatalf("seed %s: %v", ip, err)
	}
	if !inserted {
		t.Fatalf("seed %s: not inserted", ip)
	}
	return id
}

func seedVerification(t *testing.T, repo *repository.FakeProxyRepository, ip string) int64 {
	t.Helper()
	return seedRecord(t, repo, ip, contract.ProxyRecord{GeneralHealthy: true})
}

func seedRecovery(t *testing.T, repo *repository.FakeProxyRepository, ip string, now time.Time) int64 {
	t.Helper()
	return seedRecord(t, repo, ip, contract.ProxyRecord{LastVerifiedAt: &now})
}

func seedBackground(t *testing.T, repo *repository.FakeProxyRepository, ip string, now time.Time) int64 {
	t.Helper()
	old := now.Add(-2 * time.Hour)
	return seedRecord(t, repo, ip, contract.ProxyRecord{LastVerifiedAt: &old})
}

// gateChecker blocks named endpoints on a channel until the test releases
// them, records start order, and tracks per-endpoint and total concurrency.
type gateChecker struct {
	mu       sync.Mutex
	gates    map[string]chan struct{}
	started  chan string
	behavior func(ip string) (int, error)
	curTotal int
	curPerIP map[string]int
	maxTotal int
	maxPerIP int
}

func newGateChecker(behavior func(ip string) (int, error)) *gateChecker {
	return &gateChecker{
		gates:    map[string]chan struct{}{},
		started:  make(chan string, 128),
		behavior: behavior,
		curPerIP: map[string]int{},
	}
}

func (c *gateChecker) Check(ctx context.Context, _, ip string, _ int) (int, error) {
	c.mu.Lock()
	c.curTotal++
	if c.curTotal > c.maxTotal {
		c.maxTotal = c.curTotal
	}
	c.curPerIP[ip]++
	if c.curPerIP[ip] > c.maxPerIP {
		c.maxPerIP = c.curPerIP[ip]
	}
	gate := c.gates[ip]
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.curTotal--
		c.curPerIP[ip]--
		c.mu.Unlock()
	}()

	c.started <- ip
	latency, err := 42, error(nil)
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			latency, err = 0, ctx.Err()
		}
	}
	if err == nil && c.behavior != nil {
		latency, err = c.behavior(ip)
	}
	return latency, err
}

func (c *gateChecker) gate(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gates[ip] = make(chan struct{})
}

func (c *gateChecker) release(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if gate, ok := c.gates[ip]; ok {
		close(gate)
		delete(c.gates, ip)
	}
}

func (c *gateChecker) releaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ip, gate := range c.gates {
		close(gate)
		delete(c.gates, ip)
	}
}

func (c *gateChecker) peak() (total, perIP int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxTotal, c.maxPerIP
}

// attemptRecord is one CompleteScan observation as the store received it.
type attemptRecord struct {
	id      int64
	outcome contract.ScanOutcome
}

// probeStore wraps the fake repository with error injection and observability
// for the dispatcher's store interactions.
type probeStore struct {
	*repository.FakeProxyRepository

	mu             sync.Mutex
	claimCalls     int
	failClaims     int
	completeCalls  int
	failCompletes  int
	discard        bool // delegate the write, then report the fenced (false, nil) result
	releaseCalls   int
	failReleases   bool
	attempts       []attemptRecord
	completeEvents chan attemptRecord
	releaseEvents  chan struct{}
}

func newProbeStore(repo *repository.FakeProxyRepository) *probeStore {
	return &probeStore{
		FakeProxyRepository: repo,
		completeEvents:      make(chan attemptRecord, 64),
		releaseEvents:       make(chan struct{}, 64),
	}
}

func (s *probeStore) ClaimScans(ctx context.Context, queue contract.ScanQueue, limit int) ([]contract.ScanLease, error) {
	s.mu.Lock()
	s.claimCalls++
	fail := s.failClaims > 0
	if fail {
		s.failClaims--
	}
	s.mu.Unlock()
	if fail {
		return nil, errors.New("claim rpc failed")
	}
	return s.FakeProxyRepository.ClaimScans(ctx, queue, limit)
}

func (s *probeStore) CompleteScan(ctx context.Context, lease contract.ScanLease, update contract.ScanUpdate) (bool, error) {
	rec := attemptRecord{id: lease.Record.ID, outcome: update.Outcome}
	s.mu.Lock()
	s.completeCalls++
	s.attempts = append(s.attempts, rec)
	fail := s.failCompletes > 0
	if fail {
		s.failCompletes--
	}
	discard := s.discard
	s.mu.Unlock()
	if fail {
		select {
		case s.completeEvents <- rec:
		default:
		}
		return false, errors.New("complete rpc failed")
	}
	accepted, err := s.FakeProxyRepository.CompleteScan(ctx, lease, update)
	if discard && err == nil {
		accepted = false
	}
	// Signal only after the fake store applied the write so tests that read
	// row state after the event observe the persisted outcome.
	select {
	case s.completeEvents <- rec:
	default:
	}
	return accepted, err
}

func (s *probeStore) ReleaseScan(ctx context.Context, lease contract.ScanLease) error {
	s.mu.Lock()
	s.releaseCalls++
	fail := s.failReleases
	s.mu.Unlock()
	err := s.FakeProxyRepository.ReleaseScan(ctx, lease)
	if fail {
		err = errors.New("release rpc failed")
	}
	// Signal only after the fake store cleared the lease.
	select {
	case s.releaseEvents <- struct{}{}:
	default:
	}
	return err
}

func (s *probeStore) claimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimCalls
}

func (s *probeStore) attemptCountFor(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.attempts {
		if a.id == id {
			n++
		}
	}
	return n
}

func (s *probeStore) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseCalls
}

// fakeClock drives the fake repository's time source so due-ness recycles
// without wall-clock waits.
type fakeClock struct {
	nanos atomic.Int64
}

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.nanos.Store(time.Now().UnixNano())
	return c
}

func (c *fakeClock) Now() time.Time { return time.Unix(0, c.nanos.Load()) }

func (c *fakeClock) Advance(d time.Duration) { c.nanos.Add(int64(d)) }

func newWorker(store contract.ProxyScanStore, checker contract.ProxyChecker, guard contract.ProxyEndpointGuard) *ScanWorker {
	return NewScanWorker(store, checker, guard, testPolicy(), [3]int{10, 45, 45}, testLogger())
}

func shortPoll(t *testing.T) {
	t.Helper()
	original := scanPollInterval
	scanPollInterval = 15 * time.Millisecond
	t.Cleanup(func() { scanPollInterval = original })
}

// startWorker runs the worker until test cleanup cancels it and fails the
// test if the loop does not exit promptly. Shutdown-focused tests manage
// their own lifecycle to observe the return value themselves.
// startWorker runs the worker until the test ends. The returned stop function
// cancels it and waits until RunScan returned, which is after every pending
// result was finished (stored, notified, logged); calling it twice is safe.
func startWorker(t *testing.T, w *ScanWorker, concurrency int) (stop func()) {
	t.Helper()
	shortPoll(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.RunScan(ctx, concurrency)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("RunScan returned %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Error("RunScan did not exit after cancellation")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func recvStarted(t *testing.T, ch <-chan string, timeout time.Duration) string {
	t.Helper()
	select {
	case ip := <-ch:
		return ip
	case <-time.After(timeout):
		t.Fatalf("no check started within %v", timeout)
	}
	return ""
}

func waitAttempt(t *testing.T, ch <-chan attemptRecord, timeout time.Duration) attemptRecord {
	t.Helper()
	select {
	case rec := <-ch:
		return rec
	case <-time.After(timeout):
		t.Fatalf("no completion persisted within %v", timeout)
	}
	return attemptRecord{}
}

func waitSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("expected event did not occur within %v", timeout)
	}
}

func rowByID(t *testing.T, repo *repository.FakeProxyRepository, id int64) contract.ProxyRecord {
	t.Helper()
	rec, err := repo.Get(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("row %d lookup: %v", id, err)
	}
	return *rec
}

func TestRunScan_ValidatesConfiguration(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	guard := proxy.NewFakeEndpointGuard()
	checker := newGateChecker(nil)

	badWeights := [][3]int{{0, 45, 45}, {10, -1, 45}, {10, 45, 0}}
	for _, weights := range badWeights {
		w := NewScanWorker(repo, checker, guard, testPolicy(), weights, testLogger())
		if err := w.RunScan(context.Background(), 4); err == nil {
			t.Fatalf("RunScan accepted weights %v", weights)
		}
	}
	w := newWorker(repo, checker, guard)
	for _, concurrency := range []int{0, -3} {
		if err := w.RunScan(context.Background(), concurrency); err == nil {
			t.Fatalf("RunScan accepted concurrency %d", concurrency)
		}
	}
}

func TestRunScan_BlocksAtConfiguredConcurrency(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	const concurrency = 4
	const rows = 10
	ips := make([]string, 0, rows)
	for i := range rows {
		ip := fmt.Sprintf("10.1.0.%d", i+1)
		ips = append(ips, ip)
		seedVerification(t, repo, ip)
		checker.gate(ip)
	}

	w := newWorker(probe, checker, guard)
	startWorker(t, w, concurrency)

	started := make(map[string]bool)
	for range concurrency {
		started[recvStarted(t, checker.started, 2*time.Second)] = true
	}
	if len(started) != concurrency {
		t.Fatalf("distinct started checks = %d, want %d", len(started), concurrency)
	}

	// While every admitted check is gated, no fifth check may start.
	select {
	case ip := <-checker.started:
		t.Fatalf("check %s started beyond the configured concurrency", ip)
	case <-time.After(200 * time.Millisecond):
	}

	checker.releaseAll()
	for range rows - concurrency {
		recvStarted(t, checker.started, 2*time.Second)
	}

	perIP := map[string]int{}
	deadline := time.After(3 * time.Second)
	for len(perIP) < rows {
		select {
		case rec := <-probe.completeEvents:
			perIP[ipOf(t, repo, rec.id)]++
		case <-deadline:
			t.Fatalf("only %d/%d rows completed", len(perIP), rows)
		}
	}
	for _, ip := range ips {
		if perIP[ip] != 1 {
			t.Fatalf("row %s completed %d times, want exactly 1", ip, perIP[ip])
		}
	}
	if total, _ := checker.peak(); total > concurrency {
		t.Fatalf("peak concurrent checks = %d, want <= %d", total, concurrency)
	}
}

func ipOf(t *testing.T, repo *repository.FakeProxyRepository, id int64) string {
	t.Helper()
	rec, err := repo.Get(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("row %d lookup: %v", id, err)
	}
	return rec.IP
}

func TestRunScan_AllBackgroundFillsEverySlot(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	const concurrency = 4
	const rows = 6
	bgIPs := map[string]bool{}
	now := time.Now()
	for i := range rows {
		ip := fmt.Sprintf("10.2.0.%d", i+1)
		seedBackground(t, repo, ip, now)
		bgIPs[ip] = true
	}

	w := newWorker(probe, checker, guard)
	startWorker(t, w, concurrency)

	for range concurrency {
		ip := recvStarted(t, checker.started, 2*time.Second)
		if !bgIPs[ip] {
			t.Fatalf("slot filled by non-background row %s", ip)
		}
	}

	checker.releaseAll()
	waitAttempt(t, probe.completeEvents, 2*time.Second)
}

func TestRunScan_DueVerificationPreemptsBorrowedWork(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	const concurrency = 4
	now := time.Now()
	recoveryIP := "10.3.0.1"
	seedRecovery(t, repo, recoveryIP, now)
	bgIPs := []string{"10.3.1.1", "10.3.1.2", "10.3.1.3"}
	for _, ip := range bgIPs {
		seedBackground(t, repo, ip, now)
		checker.gate(ip)
	}
	checker.gate(recoveryIP)

	w := newWorker(probe, checker, guard)
	startWorker(t, w, concurrency)

	fillers := map[string]bool{}
	for range concurrency {
		fillers[recvStarted(t, checker.started, 2*time.Second)] = true
	}
	for _, ip := range append([]string{recoveryIP}, bgIPs...) {
		if !fillers[ip] {
			t.Fatalf("expected %s among the initial background/recovery fill, got %v", ip, fillers)
		}
	}

	// All slots are busy: introduce a due verification row, then free one
	// slot. The under-reservation pass must offer the verification row
	// before any further borrowing.
	verificationIP := "10.3.2.1"
	seedVerification(t, repo, verificationIP)
	checker.release(recoveryIP)
	if got := recvStarted(t, checker.started, 2*time.Second); got != verificationIP {
		t.Fatalf("freed slot admitted %s, want due verification %s", got, verificationIP)
	}

	checker.releaseAll()
	for range concurrency - 1 {
		recvStarted(t, checker.started, 2*time.Second)
	}
	waitAttempt(t, probe.completeEvents, 2*time.Second)
}

func TestRunScan_SustainsAllQueuesAtSmallConcurrency(t *testing.T) {
	for _, concurrency := range []int{1, 2} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			clock := newFakeClock()
			repo := repository.NewFakeProxyRepository(testPolicy())
			repo.Now = clock.Now
			probe := newProbeStore(repo)
			guard := proxy.NewFakeEndpointGuard()
			guard.SetHealthy(true)

			now := clock.Now()
			queueOf := map[string]string{}
			verificationIP := "10.4.0.1"
			seedVerification(t, repo, verificationIP)
			queueOf[verificationIP] = "verification"
			recoveryIP := "10.4.1.1"
			seedRecovery(t, repo, recoveryIP, now)
			queueOf[recoveryIP] = "recovery"
			backgroundIP := "10.4.2.1"
			seedBackground(t, repo, backgroundIP, now)
			queueOf[backgroundIP] = "background"

			// A target error stays inconclusive: every row keeps its queue
			// membership and becomes due again after the one-millisecond
			// inconclusive retry floor.
			checker := newGateChecker(func(string) (int, error) {
				return 0, &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 503"}
			})

			w := newWorker(probe, checker, guard)
			startWorker(t, w, concurrency)

			progress := map[string]int{}
			deadline := time.After(5 * time.Second)
			for progress["verification"] < 3 || progress["recovery"] < 3 || progress["background"] < 3 {
				clock.Advance(5 * time.Millisecond)
				select {
				case ip := <-checker.started:
					progress[queueOf[ip]]++
				case <-deadline:
					t.Fatalf("queues not sustained: %v", progress)
				}
			}
		})
	}
}

func TestRunScan_GuardPauseBlocksClaimsUntilHealthy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard() // unhealthy at startup
	checker := newGateChecker(nil)

	first := "10.5.0.1"
	second := "10.5.0.2"
	seedVerification(t, repo, first)
	seedVerification(t, repo, second)

	w := newWorker(probe, checker, guard)
	startWorker(t, w, 2)

	// The guard is unhealthy at startup: zero claims while due work exists.
	select {
	case ip := <-checker.started:
		t.Fatalf("check %s started while the guard was unhealthy", ip)
	case <-time.After(150 * time.Millisecond):
	}
	if calls := probe.claimCount(); calls != 0 {
		t.Fatalf("claim calls while guard unhealthy = %d, want 0", calls)
	}

	guard.SetHealthy(true)
	started := map[string]bool{recvStarted(t, checker.started, 2*time.Second): true}
	started[recvStarted(t, checker.started, 2*time.Second)] = true
	rec := waitAttempt(t, probe.completeEvents, 2*time.Second)
	if rec.outcome != contract.ScanSuccess {
		t.Fatalf("outcome after recovery = %v, want success", rec.outcome)
	}
}

func TestRunScan_GuardGenerationChangeDowngradesFailure(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(func(string) (int, error) {
		return 0, &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "connection refused"}
	})

	ip := "10.6.0.1"
	id := seedVerification(t, repo, ip)
	checker.gate(ip)

	w := newWorker(probe, checker, guard)
	startWorker(t, w, 1)

	if got := recvStarted(t, checker.started, 2*time.Second); got != ip {
		t.Fatalf("started %s, want %s", got, ip)
	}
	// The endpoint guard restarts while the check is in flight: generation
	// 0 -> 1 fences conclusive attribution of the proxy failure.
	guard.SetHealthy(false)
	checker.release(ip)

	rec := waitAttempt(t, probe.completeEvents, 2*time.Second)
	if rec.outcome != contract.ScanInconclusive {
		t.Fatalf("outcome after guard generation change = %v, want inconclusive", rec.outcome)
	}
	row := rowByID(t, repo, id)
	if !row.GeneralHealthy || row.FailCount != 0 {
		t.Fatalf("inconclusive attempt mutated health evidence: healthy=%v fails=%d", row.GeneralHealthy, row.FailCount)
	}
}

func TestRunScan_PersistenceFailurePausesThenRecovers(t *testing.T) {
	repo := repository.NewFakeProxyRepository(shortLeasePolicy())
	probe := newProbeStore(repo)
	probe.mu.Lock()
	probe.failCompletes = 1
	probe.mu.Unlock()
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	ip := "10.7.0.1"
	id := seedVerification(t, repo, ip)

	w := newWorker(probe, checker, guard)
	startWorker(t, w, 1)

	first := waitAttempt(t, probe.completeEvents, 2*time.Second)
	if first.outcome != contract.ScanSuccess {
		t.Fatalf("first attempt outcome = %v, want success", first.outcome)
	}
	// The failed write keeps the lease; expiry must hand the row back and the
	// loop must continue without reclassifying the proxy.
	second := waitAttempt(t, probe.completeEvents, 3*time.Second)
	if second.id != first.id {
		t.Fatalf("retry claimed row %d, want %d", second.id, first.id)
	}
	row := rowByID(t, repo, id)
	if !row.GeneralHealthy {
		t.Fatal("row not recovered after lease expiry")
	}
}

func TestRunScan_ClaimErrorPausesThenContinues(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	probe.mu.Lock()
	probe.failClaims = 2
	probe.mu.Unlock()
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	ip := "10.8.0.1"
	seedVerification(t, repo, ip)

	w := newWorker(probe, checker, guard)
	startWorker(t, w, 1)

	waitAttempt(t, probe.completeEvents, 3*time.Second)
	if calls := probe.claimCount(); calls < 3 {
		t.Fatalf("claim calls = %d, want >= 3 after two failures", calls)
	}
}

func TestRunScan_NotifierOnlyOnAcceptedSuccess(t *testing.T) {
	t.Run("accepted success notifies", func(t *testing.T) {
		repo := repository.NewFakeProxyRepository(testPolicy())
		probe := newProbeStore(repo)
		guard := proxy.NewFakeEndpointGuard()
		guard.SetHealthy(true)
		checker := newGateChecker(nil)

		ip := "10.9.0.1"
		seedVerification(t, repo, ip)
		w := newWorker(probe, checker, guard)
		// The worker notifies after the store reported the completion, so
		// the test waits on the notifier itself, never on a sleep or on the
		// store's event alone.
		notified := make(chan struct{}, 4)
		w.SetNotifier(func() { notified <- struct{}{} })
		stop := startWorker(t, w, 1)

		rec := waitAttempt(t, probe.completeEvents, 2*time.Second)
		if rec.outcome != contract.ScanSuccess {
			t.Fatalf("outcome = %v, want success", rec.outcome)
		}
		select {
		case <-notified:
		case <-time.After(2 * time.Second):
			t.Fatal("no notification after an accepted success")
		}
		stop()
		if extra := len(notified); extra != 0 {
			t.Fatalf("%d extra notifications for one accepted success, want none", extra)
		}
	})

	t.Run("accepted failure does not notify", func(t *testing.T) {
		repo := repository.NewFakeProxyRepository(testPolicy())
		probe := newProbeStore(repo)
		guard := proxy.NewFakeEndpointGuard()
		guard.SetHealthy(true)
		now := time.Now()
		ip := "10.9.1.1"
		id := seedRecovery(t, repo, ip, now)
		checker := newGateChecker(func(string) (int, error) {
			return 0, &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "connection refused"}
		})

		w := newWorker(probe, checker, guard)
		var notifications atomic.Int32
		w.SetNotifier(func() { notifications.Add(1) })
		stop := startWorker(t, w, 1)

		rec := waitAttempt(t, probe.completeEvents, 2*time.Second)
		if rec.outcome != contract.ScanFailure {
			t.Fatalf("outcome = %v, want conclusive failure", rec.outcome)
		}
		stop() // the attempt is fully finished once RunScan returned
		if notifications.Load() != 0 {
			t.Fatalf("notifications = %d, want 0 for accepted failure", notifications.Load())
		}
		row := rowByID(t, repo, id)
		if row.GeneralHealthy || row.FailCount != 1 {
			t.Fatalf("failure not persisted: healthy=%v fails=%d", row.GeneralHealthy, row.FailCount)
		}
	})

	t.Run("discarded completion does not notify", func(t *testing.T) {
		repo := repository.NewFakeProxyRepository(testPolicy())
		probe := newProbeStore(repo)
		probe.mu.Lock()
		probe.discard = true
		probe.mu.Unlock()
		guard := proxy.NewFakeEndpointGuard()
		guard.SetHealthy(true)
		ip := "10.9.2.1"
		seedVerification(t, repo, ip)
		checker := newGateChecker(func(string) (int, error) {
			return 0, &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 503"}
		})

		w := newWorker(probe, checker, guard)
		var notifications atomic.Int32
		w.SetNotifier(func() { notifications.Add(1) })
		stop := startWorker(t, w, 1)

		for range 3 {
			waitAttempt(t, probe.completeEvents, 2*time.Second)
		}
		stop() // every attempt is fully finished once RunScan returned
		if notifications.Load() != 0 {
			t.Fatalf("notifications = %d, want 0 for discarded completions", notifications.Load())
		}
	})
}

func TestRunScan_ShutdownReleasesLeasesWithoutFailureAttribution(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	ips := []string{"10.10.0.1", "10.10.0.2"}
	ids := map[string]int64{}
	for _, ip := range ips {
		ids[ip] = seedVerification(t, repo, ip)
		checker.gate(ip)
	}

	shortPoll(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w := newWorker(probe, checker, guard)
	go func() {
		done <- w.RunScan(ctx, 2)
	}()

	for range ips {
		recvStarted(t, checker.started, 2*time.Second)
	}
	cancel() // process-style cancellation while checks are blocked
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunScan returned %v on cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunScan did not exit after cancellation")
	}

	for _, ip := range ips {
		row := rowByID(t, repo, ids[ip])
		if row.ScanToken != nil {
			t.Fatalf("row %s still leased after shutdown", ip)
		}
		if !row.GeneralHealthy || row.FailCount != 0 {
			t.Fatalf("shutdown attributed a failure to %s: healthy=%v fails=%d", ip, row.GeneralHealthy, row.FailCount)
		}
	}
}

func TestRunScan_ShutdownReleaseFailureStillExits(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	probe.mu.Lock()
	probe.failReleases = true
	probe.mu.Unlock()
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)
	checker := newGateChecker(nil)

	ip := "10.11.0.1"
	seedVerification(t, repo, ip)
	checker.gate(ip)

	shortPoll(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	w := newWorker(probe, checker, guard)
	go func() {
		done <- w.RunScan(ctx, 1)
	}()

	recvStarted(t, checker.started, 2*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunScan returned %v despite a release error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunScan hung after a release persistence failure")
	}
	if calls := probe.releaseCount(); calls == 0 {
		t.Fatal("shutdown did not attempt to release the outstanding lease")
	}
}

func TestRunScan_PanicReleasesLeaseAndCapacity(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)

	panicIP := "10.12.0.1"
	okIP := "10.12.0.2"
	panicID := seedVerification(t, repo, panicIP)
	seedVerification(t, repo, okIP)
	checker := newGateChecker(func(ip string) (int, error) {
		if ip == panicIP {
			panic("checker exploded")
		}
		return 42, nil
	})

	w := newWorker(probe, checker, guard)
	startWorker(t, w, 2)

	recvStarted(t, checker.started, 2*time.Second)
	recvStarted(t, checker.started, 2*time.Second)

	rec := waitAttempt(t, probe.completeEvents, 2*time.Second)
	if rec.id == 0 || rec.outcome != contract.ScanSuccess {
		t.Fatalf("sibling check did not complete normally: %+v", rec)
	}
	if attempts := probe.attemptCountFor(panicID); attempts != 0 {
		t.Fatalf("panicked check persisted %d attempts, want 0", attempts)
	}
	row := rowByID(t, repo, panicID)
	if !row.GeneralHealthy || row.FailCount != 0 {
		t.Fatalf("panic marked the proxy dead: healthy=%v fails=%d", row.GeneralHealthy, row.FailCount)
	}
	// The panicked row is the only releasing row in this test, so the next
	// release event is deterministically the panicked branch's ReleaseScan.
	waitSignal(t, probe.releaseEvents, 2*time.Second)
}

func TestRunScan_NoDoubleOwnershipUnderContinuousReadmission(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	probe := newProbeStore(repo)
	guard := proxy.NewFakeEndpointGuard()
	guard.SetHealthy(true)

	const concurrency = 4
	const rows = 8
	now := time.Now()
	for i := range rows {
		seedBackground(t, repo, fmt.Sprintf("10.13.0.%d", i+1), now)
	}
	checker := newGateChecker(func(string) (int, error) {
		return 0, &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "status 503"}
	})

	w := newWorker(probe, checker, guard)
	startWorker(t, w, concurrency)

	for range 4 * rows {
		waitAttempt(t, probe.completeEvents, 2*time.Second)
	}

	total, perIP := checker.peak()
	if total > concurrency {
		t.Fatalf("peak concurrent checks = %d, want <= %d", total, concurrency)
	}
	if perIP != 1 {
		t.Fatalf("peak concurrent checks of one row = %d, want 1", perIP)
	}
}
