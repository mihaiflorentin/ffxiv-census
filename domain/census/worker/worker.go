package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// jobIdentity carries the identifying payload fields for lifecycle logs so
// any job can be traced by character id or id range regardless of type.
type jobIdentity struct {
	From        uint32 `json:"from"`
	To          uint32 `json:"to"`
	CharacterID uint32 `json:"character_id"`
}

// jobAttrs extracts identifying payload fields for log records. Unknown or
// malformed payloads log without identity attributes.
func jobAttrs(payload []byte) []any {
	var id jobIdentity
	if err := json.Unmarshal(payload, &id); err != nil {
		return nil
	}
	var attrs []any
	if id.From != 0 || id.To != 0 {
		attrs = append(attrs,
			slog.Uint64("from", uint64(id.From)),
			slog.Uint64("to", uint64(id.To)))
	}
	if id.CharacterID != 0 {
		attrs = append(attrs, slog.Uint64("character_id", uint64(id.CharacterID)))
	}
	return attrs
}

// Worker consumes jobs from configured event types and dispatches them to registered
// handlers using push-based consumption from the queue.
type Worker struct {
	queue       contract.Queue
	handlers    *handler.Registry
	logger      contract.Logger
	rateLimiter contract.ProviderRateLimiter

	maxProxyAttempts int // proxies one job may try in-process; 0 = default
}

func New(q contract.Queue, h *handler.Registry, logger contract.Logger, rateLimiter ...contract.ProviderRateLimiter) *Worker {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	var rl contract.ProviderRateLimiter
	if len(rateLimiter) > 0 {
		rl = rateLimiter[0]
	}
	return &Worker{
		queue:       q,
		handlers:    h,
		logger:      logger,
		rateLimiter: rl,
	}
}

// idleConnectionCloser is an optional interface for HTTP clients that support
// closing idle connections. This is a private structural interface so port
// contracts and fake clients do not gain a lifecycle method.
type idleConnectionCloser interface {
	CloseIdleConnections()
}

// closeIdleConnections closes idle connections if the client supports it.
func closeIdleConnections(client any) {
	if closer, ok := client.(idleConnectionCloser); ok {
		closer.CloseIdleConnections()
	}
}

func (w *Worker) Run(ctx context.Context, eventType string, concurrency int) error {
	return w.RunEvents(ctx, []string{eventType}, concurrency)
}

// RunEvents starts push-based consumption for all configured event types.
// The queue delivers messages to the handler; retry and dead-letter logic
// is handled internally by the queue adapter.
func (w *Worker) RunEvents(ctx context.Context, eventTypes []string, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 4
	}
	if len(eventTypes) == 0 {
		eventTypes = []string{
			handler.EventIDSweep,
			handler.EventCharacterCensus,
			handler.EventAchievementCensus,
		}
	}

	for _, eventType := range eventTypes {
		// Skip validation for failed queue names — they don't have handlers,
		// but messages from failed queues have the original event type as routing key.
		if strings.HasSuffix(eventType, ".failed") {
			continue
		}
		if _, ok := w.handlers.Get(eventType); !ok {
			return fmt.Errorf("no handler registered for event %q", eventType)
		}
	}

	w.logger.InfoContext(ctx, "worker.start", slog.Any("event_types", eventTypes), slog.Int("concurrency", concurrency))

	processJob := func(processCtx context.Context, job contract.QueueJob) error {
		h, ok := w.handlers.Get(job.Type)
		if !ok {
			w.logger.ErrorContext(processCtx, "worker.missing_handler", slog.String("event_type", job.Type))
			return missingHandlerError(job.Type)
		}

		// Wait for required providers to become available before processing.
		// Use processCtx so a claimed job is not aborted by SIGTERM during
		// a rate-limit wait; cancellation only stops new claims.
		if w.rateLimiter != nil {
			if err := w.waitForProviders(processCtx, job.Type); err != nil {
				return err
			}
		}

		start := time.Now()
		w.logger.DebugContext(
			processCtx, "worker.job_start",
			slog.String("event_type", job.Type),
		)

		var next []contract.QueueJob
		var err error

		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("worker panic: %v\nstack: %s", r, debug.Stack())
				}
			}()
			next, err = h.Handle(processCtx, job.Payload)
		}()

		if err != nil {
			w.logger.ErrorContext(
				processCtx, "worker.job_retry",
				append([]any{
					slog.String("event_type", job.Type),
					slog.Duration("duration", time.Since(start)),
					slog.Any("error", err),
				}, jobAttrs(job.Payload)...)...,
			)
			if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate limit") {
				select {
				case <-processCtx.Done():
					return err
				case <-time.After(200 * time.Millisecond):
				}
			}
			return err
		}

		// Publish downstream jobs individually.
		for _, nextJob := range next {
			if pubErr := w.queue.Publish(processCtx, nextJob); pubErr != nil {
				w.logger.ErrorContext(
					processCtx, "worker.publish_error",
					slog.String("event_type", nextJob.Type),
					slog.Any("error", pubErr),
				)
				return pubErr
			}
		}

		w.logger.InfoContext(
			processCtx, "worker.job_done",
			append([]any{
				slog.String("event_type", job.Type),
				slog.Duration("duration", time.Since(start)),
				slog.Int("chained", len(next)),
			}, jobAttrs(job.Payload)...)...,
		)

		return nil
	}

	err := w.queue.Consume(ctx, eventTypes, concurrency, processJob)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// waitForProviders blocks until the Lodestone provider is available. All
// census events are Lodestone-only; The Lodestone is the sole data source.
func (w *Worker) waitForProviders(ctx context.Context, eventType string) error {
	// Failed queue retries don't need provider checks — the original handler
	// will check providers when it processes the message.
	if strings.HasSuffix(eventType, ".failed") {
		return nil
	}
	if !w.rateLimiter.IsAvailable(contract.ProviderLodestone) {
		w.logger.InfoContext(ctx, "worker.waiting_for_provider", slog.String("event_type", eventType), slog.String("provider", "lodestone"))
		return w.rateLimiter.WaitUntilAvailable(ctx, contract.ProviderLodestone)
	}
	return nil
}

// isGeneralProxyFailure reports whether the error is a typed conclusive
// proxy failure (*contract.ProxyCheckError with Kind CheckProxy), possibly
// wrapped. Only proven proxy dial/negotiation failures kill general health;
// deadline, TLS and read errors are ambiguous on this unguarded destination
// path and stay ordinary job errors. Decisions use errors.As on the typed
// fields, never error text.
func isGeneralProxyFailure(err error) bool {
	var checkErr *contract.ProxyCheckError
	return errors.As(err, &checkErr) && checkErr.Kind == contract.CheckProxy
}

// proxyFailureAction is what the consumer does about a failed job attempt.
type proxyFailureAction uint8

const (
	// proxyFailureNone: an ordinary job outcome (genuine 404/403, parse or
	// business error, cancellation). No proxy write, no rotation.
	proxyFailureNone proxyFailureAction = iota
	// proxyFailureCooldown: the destination rate-limits this identity (no
	// challenge, no 5xx). Cool the proxy and return the delivery to the
	// queue; the provider-wide pause already applies.
	proxyFailureCooldown
	// proxyFailureRotate: a challenge, timeout or 5xx. Cool the proxy and
	// retry the same job in-process on a different one.
	proxyFailureRotate
	// proxyFailureDead: a proven proxy dial/negotiation failure. Persist the
	// general failure and retry in-process on a different proxy.
	proxyFailureDead
)

// classifyProxyFailure maps a job error to a proxyFailureAction using the
// typed *contract.ProxyCheckError only (errors.As), never error text.
func classifyProxyFailure(err error) proxyFailureAction {
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) {
		return proxyFailureNone
	}
	switch checkErr.Kind {
	case contract.CheckProxy:
		return proxyFailureDead
	case contract.CheckDeadline:
		return proxyFailureRotate
	case contract.CheckTarget:
		if checkErr.Challenge || checkErr.Status >= http.StatusInternalServerError {
			return proxyFailureRotate
		}
		return proxyFailureCooldown
	default:
		return proxyFailureNone
	}
}

// defaultProxyAttempts is how many proxies a job may try in-process before it
// takes the normal queue error path.
const defaultProxyAttempts = 3

// WithProxyAttempts sets the maximum number of proxies one job tries
// in-process (first attempt included). Values below one keep the default.
func (w *Worker) WithProxyAttempts(n int) *Worker {
	if n > 0 {
		w.maxProxyAttempts = n
	}
	return w
}

func (w *Worker) proxyAttempts() int {
	if w.maxProxyAttempts > 0 {
		return w.maxProxyAttempts
	}
	return defaultProxyAttempts
}

// recordProxyFailure persists the proxy-side consequence of a failed attempt
// in a single write: a dial failure becomes a fenced general failure
// (Proxy.MarkFailed); every destination failure raises the destination
// cooldown, which grows with the proxy's consecutive failures, and releases
// the claim.
func (w *Worker) recordProxyFailure(
	ctx context.Context,
	action proxyFailureAction,
	jobErr error,
	proxy *proxydomain.Proxy,
	owner string,
	proxyHub *proxydomain.ProxyHub,
) error {
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lockTTL := proxyHub.LockTTL()
	if action == proxyFailureDead {
		w.logger.InfoContext(ctx, "worker.proxy_bad", slog.String("proxy", proxy.Address()))
		if _, err := proxy.MarkFailed(writeCtx, owner, lockTTL); err != nil {
			return fmt.Errorf("record proxy failure: %w", err)
		}
		return nil
	}
	var hint time.Duration
	var checkErr *contract.ProxyCheckError
	if errors.As(jobErr, &checkErr) {
		hint = checkErr.RetryAfter
	}
	delay := proxyHub.CooldownFor(proxy, hint)
	w.logger.InfoContext(ctx, "worker.proxy_cooldown",
		slog.String("proxy", proxy.Address()),
		slog.Int("consecutive_failures", proxy.Record().DestinationFailures+1),
		slog.Duration("cooldown", delay))
	if err := proxy.CooldownDestination(writeCtx, owner, lockTTL, delay); err != nil {
		return fmt.Errorf("destination cooldown: %w", err)
	}
	return nil
}

// missingHandlerError builds the sentinel-classified error for a delivery
// whose event type has no registered handler. Wrapping contract.ErrNoHandler
// lets the queue adapter dead-park the delivery: no process in the
// deployment can ever handle the event, so retrying would never succeed.
func missingHandlerError(eventType string) error {
	return fmt.Errorf("%w: no handler registered for event %s", contract.ErrNoHandler, eventType)
}

// It retries on nil results and transient acquisition errors with exponential
// backoff (5s → 10s → 20s → 40s → 60s cap), logging only on the first attempt
// and when backoff reaches the cap. This prevents CPU and DB burn when fewer
// active proxies exist than configured goroutines.
func (w *Worker) waitForProxy(ctx context.Context, owner string, proxyHub *proxydomain.ProxyHub) (*proxydomain.Proxy, error) {
	backoff := 5 * time.Second
	const maxBackoff = 60 * time.Second
	firstAttempt := true
	capLogged := false

	for {
		p, err := proxyHub.NewProxy(ctx, owner)
		if err == nil && p != nil {
			return p, nil
		}

		if firstAttempt {
			if err != nil {
				w.logger.WarnContext(ctx, "worker.proxy_waiting", slog.String("owner", owner), slog.Any("error", err))
			} else {
				w.logger.InfoContext(ctx, "worker.proxy_waiting", slog.String("owner", owner), slog.String("reason", "no proxy available"))
			}
			firstAttempt = false
		} else if !capLogged && backoff >= maxBackoff {
			w.logger.InfoContext(ctx, "worker.proxy_waiting_backoff", slog.String("owner", owner), slog.Duration("backoff", backoff))
			capLogged = true
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		case <-proxyHub.NotifyCh():
			timer.Stop()
			// Proxy became available — reset backoff and retry immediately.
			backoff = 5 * time.Second
			continue
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// replaceProxy closes the previous client's idle connections, releases the
// previous proxy claim, acquires a replacement, and builds new
// clients/handlers. The previous claim is released before the
// replacement is acquired so a pool with no spare capacity can rotate; a
// window without a proxy is bounded by the release being local. Release
// errors are logged but do not prevent the replacement from being used.
// A typed general failure is persisted by the caller via Proxy.MarkFailed
// before the replacement is acquired; this helper only gives up the claim.
func (w *Worker) replaceProxy(
	ctx context.Context,
	previous *proxydomain.Proxy,
	previousClient contract.LodestoneClient,
	owner string,
	proxyHub *proxydomain.ProxyHub,
	newLodestoneClient func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error),
	newRateLimiter func() contract.ProviderRateLimiter,
	newHandlers func(contract.LodestoneClient, contract.ProviderRateLimiter) *handler.Registry,
) (*proxydomain.Proxy, contract.LodestoneClient, contract.ProviderRateLimiter, *handler.Registry, error) {
	// The replaced client owns an idle tunnel to the old proxy that would
	// otherwise linger until its idle timeout.
	closeIdleConnections(previousClient)
	if previous != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if relErr := previous.Release(cleanupCtx, owner); relErr != nil {
			w.logger.WarnContext(cleanupCtx, "worker.proxy_release_error",
				slog.String("proxy", previous.Address()), slog.Any("error", relErr))
		}
		cancel()
		// Wake one waiting worker now that a proxy slot is free.
		proxyHub.NotifyAvailable()
	}

	// Acquire replacement — now the previous slot is free.
	replacement, err := w.waitForProxy(ctx, owner, proxyHub)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("no checked proxy available: %w", err)
	}

	// Build new clients/handlers for the replacement.
	proxyLimiter := newRateLimiter()
	lodestoneClient, err := newLodestoneClient(replacement.Address(), proxyLimiter)
	if err != nil {
		if relErr := replacement.Release(context.Background(), owner); relErr != nil {
			w.logger.WarnContext(context.Background(), "worker.proxy_cleanup_release_error",
				slog.String("proxy", replacement.Address()), slog.Any("error", relErr))
		}
		return nil, nil, nil, nil, fmt.Errorf("create lodestone client: %w", err)
	}
	handlers := newHandlers(lodestoneClient, proxyLimiter)

	return replacement, lodestoneClient, proxyLimiter, handlers, nil
}

// RunEventsWithProxy runs the worker pool with per-goroutine proxy lifecycle.
// Each goroutine acquires its own proxy from the ProxyHub, creates proxy-aware
// clients, and uses them for all requests. If a proxy's ownership changes
// (CanUse returns false), the goroutine acquires a new proxy and retries the
// job in-place.
func (w *Worker) RunEventsWithProxy(
	ctx context.Context,
	eventTypes []string,
	concurrency int,
	ownerPrefix string,
	proxyHub *proxydomain.ProxyHub,
	newHandlers func(lodestone contract.LodestoneClient, rateLimiter contract.ProviderRateLimiter) *handler.Registry,
	newLodestoneClient func(proxyURL string, limiter contract.ProviderRateLimiter) (contract.LodestoneClient, error),
	newRateLimiter func() contract.ProviderRateLimiter,
) error {
	if concurrency <= 0 {
		concurrency = 4
	}
	if len(eventTypes) == 0 {
		eventTypes = []string{
			handler.EventIDSweep,
			handler.EventCharacterCensus,
			handler.EventAchievementCensus,
		}
	}

	w.logger.InfoContext(ctx, "worker.proxy_start", slog.Any("event_types", eventTypes), slog.Int("concurrency", concurrency))

	childCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := range concurrency {
		workerID := i
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			if err := w.proxyWorkerLoop(ctx, childCtx, eventTypes, wid, ownerPrefix, proxyHub, newHandlers, newLodestoneClient, newRateLimiter); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.ErrorContext(childCtx, "worker.proxy_loop_error", slog.Int("worker_id", wid), slog.Any("error", err))
				errCh <- err
			}
		}(workerID)
	}

	wg.Wait()
	close(errCh)
	w.logger.InfoContext(ctx, "worker.proxy_stop", slog.Any("event_types", eventTypes))

	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// proxyWorkerLoop is the main loop for a single proxy-mode worker goroutine.
func (w *Worker) proxyWorkerLoop(
	claimCtx context.Context,
	processCtx context.Context,
	eventTypes []string,
	workerID int,
	ownerPrefix string,
	proxyHub *proxydomain.ProxyHub,
	newHandlers func(lodestone contract.LodestoneClient, rateLimiter contract.ProviderRateLimiter) *handler.Registry,
	newLodestoneClient func(proxyURL string, limiter contract.ProviderRateLimiter) (contract.LodestoneClient, error),
	newRateLimiter func() contract.ProviderRateLimiter,
) error {
	owner := fmt.Sprintf("%s-w%d", ownerPrefix, workerID)

	// Acquire initial proxy — wait if none available.
	proxy, err := w.waitForProxy(claimCtx, owner, proxyHub)
	if err != nil {
		return fmt.Errorf("proxy acquire: %w", err)
	}
	w.logger.InfoContext(claimCtx, "worker.proxy_acquired", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.String("owner", owner))

	// Create proxy-aware client and handlers.
	proxyLimiter := newRateLimiter()
	lodestoneClient, err := newLodestoneClient(proxy.Address(), proxyLimiter)
	if err != nil {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer releaseCancel()
		if relErr := proxy.Release(releaseCtx, owner); relErr != nil {
			w.logger.WarnContext(releaseCtx, "worker.proxy_cleanup_release_error", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.Any("error", relErr))
			return errors.Join(fmt.Errorf("create lodestone client: %w", err), fmt.Errorf("release proxy: %w", relErr))
		}
		return fmt.Errorf("create lodestone client: %w", err)
	}
	handlers := newHandlers(lodestoneClient, proxyLimiter)

	defer func() {
		closeIdleConnections(lodestoneClient)
		if proxy != nil {
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer releaseCancel()
			if err := proxy.Release(releaseCtx, owner); err != nil {
				w.logger.WarnContext(releaseCtx, "worker.proxy_release_error", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.Any("error", err))
			} else {
				w.logger.InfoContext(releaseCtx, "worker.proxy_released", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.String("owner", owner))
				proxyHub.NotifyAvailable()
			}
		}
	}()

	// proxyWaitForProviders applies the same event-aware provider wait used by
	// waitForProviders, but using the proxy's own rate limiter.
	proxyWaitForProviders := func(ctx context.Context, eventType string) error {
		if proxyLimiter == nil {
			return nil
		}
		if strings.HasSuffix(eventType, ".failed") {
			return nil
		}
		if !proxyLimiter.IsAvailable(contract.ProviderLodestone) {
			w.logger.InfoContext(ctx, "worker.waiting_for_provider", slog.String("event_type", eventType), slog.String("provider", "lodestone"))
			return proxyLimiter.WaitUntilAvailable(ctx, contract.ProviderLodestone)
		}
		return nil
	}

	// attemptJob runs the job once through the current proxy's handlers:
	// event-aware provider wait, handler lookup and panic recovery. It reads
	// the loop's current handlers/proxy, so a rotation between attempts is
	// picked up automatically.
	attemptJob := func(ctx context.Context, job contract.QueueJob, attempt int) (next []contract.QueueJob, err error) {
		if err := proxyWaitForProviders(ctx, job.Type); err != nil {
			return nil, err
		}
		h, ok := handlers.Get(job.Type)
		if !ok {
			w.logger.ErrorContext(ctx, "worker.missing_handler", slog.String("event_type", job.Type))
			return nil, missingHandlerError(job.Type)
		}
		w.logger.DebugContext(
			ctx, "worker.job_start",
			slog.String("event_type", job.Type),
			slog.Int("worker_id", workerID),
			slog.Int("attempt", attempt),
			slog.String("proxy", proxy.Address()),
			slog.String("owner", owner),
		)
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("worker panic: %v\nstack: %s", r, debug.Stack())
			}
		}()
		return h.Handle(ctx, job.Payload)
	}

	processJob := func(ctx context.Context, job contract.QueueJob) error {
		// Check proxy ownership and extend the lock.
		// Use a short-lived context so shutdown doesn't block lock extension.
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		lockTTL := proxyHub.LockTTL()
		canUse, canErr := proxy.CanUse(lockCtx, owner, lockTTL)
		lockCancel()
		if canErr != nil {
			// Database error — return for queue retry, retain current proxy.
			w.logger.WarnContext(ctx, "worker.proxy_canuse_error", slog.Int("worker_id", workerID), slog.Any("error", canErr))
			return fmt.Errorf("proxy canuse check: %w", canErr)
		}
		if !canUse {
			w.logger.InfoContext(ctx, "worker.proxy_lost", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.String("owner", owner))
			newProxy, newLodestone, newLimiter, newReg, rerr := w.replaceProxy(
				ctx, proxy, lodestoneClient, owner, proxyHub,
				newLodestoneClient, newRateLimiter, newHandlers,
			)
			if rerr != nil {
				return fmt.Errorf("proxy re-acquire: %w", rerr)
			}
			proxy = newProxy
			lodestoneClient = newLodestone
			proxyLimiter = newLimiter
			handlers = newReg
			w.logger.InfoContext(ctx, "worker.proxy_reacquired", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.String("owner", owner))
		}

		start := time.Now()
		var next []contract.QueueJob
		var jobErr error
		for attempt := 1; ; attempt++ {
			next, jobErr = attemptJob(ctx, job, attempt)
			if jobErr == nil {
				break
			}
			w.logger.WarnContext(
				ctx, "worker.job_retry",
				append([]any{
					slog.String("event_type", job.Type),
					slog.Int("worker_id", workerID),
					slog.Int("attempt", attempt),
					slog.String("proxy", proxy.Address()),
					slog.Duration("duration", time.Since(start)),
					slog.Any("error", jobErr),
				}, jobAttrs(job.Payload)...)...,
			)

			action := classifyProxyFailure(jobErr)
			if action == proxyFailureNone {
				return jobErr
			}
			if err := w.recordProxyFailure(ctx, action, jobErr, proxy, owner, proxyHub); err != nil {
				return err
			}
			if action == proxyFailureCooldown || attempt >= w.proxyAttempts() {
				// A rate-limited destination returns to the queue's retry
				// path; an exhausted job does too. The claim is already
				// given up (cooldown) or released below (dial failure); the
				// next job re-acquires through the proxy-lost path.
				if action == proxyFailureDead {
					releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
					if relErr := proxy.Release(releaseCtx, owner); relErr != nil {
						w.logger.WarnContext(releaseCtx, "worker.proxy_release_error",
							slog.String("proxy", proxy.Address()), slog.Any("error", relErr))
					}
					releaseCancel()
				}
				return jobErr
			}

			newProxy, newLodestone, newLimiter, newReg, rerr := w.replaceProxy(
				ctx, proxy, lodestoneClient, owner, proxyHub,
				newLodestoneClient, newRateLimiter, newHandlers,
			)
			if rerr != nil {
				return fmt.Errorf("proxy re-acquire after failure: %w", rerr)
			}
			proxy = newProxy
			lodestoneClient = newLodestone
			proxyLimiter = newLimiter
			handlers = newReg
			w.logger.InfoContext(ctx, "worker.proxy_reacquired", slog.Int("worker_id", workerID), slog.String("proxy", proxy.Address()), slog.String("owner", owner))
		}

		// The job worked through this proxy: a failure streak is over. The
		// record was refreshed by CanUse (or came with the claim), so a
		// streak-free proxy costs no write here.
		if proxy.Record().DestinationFailures > 0 {
			resetCtx, resetCancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := proxy.ResetDestinationFailures(resetCtx, owner); err != nil {
				w.logger.WarnContext(resetCtx, "worker.proxy_reset_failures_error",
					slog.String("proxy", proxy.Address()), slog.Any("error", err))
			}
			resetCancel()
		}

		// Publish downstream jobs individually.
		for _, nextJob := range next {
			if pubErr := w.queue.Publish(ctx, nextJob); pubErr != nil {
				w.logger.ErrorContext(
					ctx, "worker.publish_error",
					slog.String("event_type", nextJob.Type),
					slog.Any("error", pubErr),
				)
				return pubErr
			}
		}

		w.logger.InfoContext(
			ctx, "worker.job_done",
			append([]any{
				slog.String("event_type", job.Type),
				slog.Duration("duration", time.Since(start)),
				slog.Int("chained", len(next)),
			}, jobAttrs(job.Payload)...)...,
		)

		return nil
	}

	return w.queue.Consume(claimCtx, eventTypes, 1, processJob)
}
