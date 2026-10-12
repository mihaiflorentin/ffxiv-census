package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const (
	exchangeMain     = "census"
	queuePrefix      = "census."
	headerAttempts   = "x-attempts"
	slowAckThreshold = 30 * time.Second

	// headerProxyFailures counts failures attributed to the proxy. It is
	// informational: only headerAttempts drives tier selection and the
	// attempt cap.
	headerProxyFailures = "x-proxy-failures"
	// headerNotBefore is set only by the pre-tier retry path. It is still
	// read to move messages left in census.<type>.failed into a tier.
	headerNotBefore = "x-not-before-ms"
)

// ErrInvalidRetryTiers reports a delay-tier list the broker cannot use.
var ErrInvalidRetryTiers = errors.New("invalid retry tiers")

// Queue is a RabbitMQ-backed work queue implementing contract.Queue.
type Queue struct {
	url         string
	conn        *amqp.Connection
	ch          *amqp.Channel
	returns     <-chan amqp.Return
	mu          sync.Mutex
	logger      contract.Logger
	maxAttempts int
	tiers       []time.Duration
}

// Option configures a Queue.
type Option func(*Queue)

// WithRetryTiers sets the delay tiers a retried job waits in (default
// contract.DefaultRetryTiers). The list must be strictly ascending, each at
// least one millisecond. Every tier is its own queue named after its delay,
// so changing the list declares new queues and leaves the old ones to expire
// their messages back to the main queue; it never conflicts with an existing
// declaration.
func WithRetryTiers(tiers []time.Duration) Option {
	return func(q *Queue) { q.tiers = append([]time.Duration(nil), tiers...) }
}

// validateTiers checks that tiers form a usable ascending ladder.
func validateTiers(tiers []time.Duration) error {
	if len(tiers) == 0 {
		return fmt.Errorf("%w: at least one tier is required", ErrInvalidRetryTiers)
	}
	var prev time.Duration
	for _, d := range tiers {
		if d < time.Millisecond {
			return fmt.Errorf("%w: tier %s is below one millisecond", ErrInvalidRetryTiers, d)
		}
		if d <= prev {
			return fmt.Errorf("%w: tier %s is not above the previous tier %s", ErrInvalidRetryTiers, d, prev)
		}
		prev = d
	}
	return nil
}

// openSession dials the broker, opens a channel, declares the full topology,
// enables publisher confirms, and registers a return listener. On any setup
// error the partially opened channel/connection are closed and the error is
// returned without modifying the queue state.
func (q *Queue) openSession() (*amqp.Connection, *amqp.Channel, <-chan amqp.Return, error) {
	conn, err := amqp.Dial(q.url)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rabbitmq dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, nil, nil, fmt.Errorf("rabbitmq channel: %w", err)
	}

	if err := q.declareTopology(ch); err != nil {
		ch.Close()
		conn.Close()
		return nil, nil, nil, fmt.Errorf("rabbitmq topology: %w", err)
	}

	if err := ch.Confirm(false); err != nil {
		ch.Close()
		conn.Close()
		return nil, nil, nil, fmt.Errorf("rabbitmq confirm: %w", err)
	}

	rets := ch.NotifyReturn(make(chan amqp.Return, 1))

	return conn, ch, rets, nil
}

// New creates a new RabbitMQ Queue. It dials the broker, declares the full
// topology (exchanges, queues, bindings), enables publisher confirms, and
// returns a ready-to-use Queue. maxAttempts is the safety net: a delivery
// whose job-attributable failure count reaches it is dead-parked instead of
// retried; it must be at least 1.
func New(url string, logger contract.Logger, maxAttempts int, opts ...Option) (*Queue, error) {
	if maxAttempts < 1 {
		return nil, fmt.Errorf("rabbitmq max attempts must be >= 1, got %d", maxAttempts)
	}
	q := &Queue{url: url, logger: logger, maxAttempts: maxAttempts, tiers: contract.DefaultRetryTiers()}
	for _, opt := range opts {
		opt(q)
	}
	if err := validateTiers(q.tiers); err != nil {
		return nil, err
	}
	conn, ch, rets, err := q.openSession()
	if err != nil {
		return nil, err
	}
	q.conn = conn
	q.ch = ch
	q.returns = rets
	return q, nil
}

// queueSpec describes one queue the adapter declares.
type queueSpec struct {
	name string
	args amqp.Table
	// bindKey, when set, binds the queue to the main exchange under it. Side
	// queues are published to through the default exchange and are unbound.
	bindKey string
}

// topology lists every queue per event type: the main queue, one delay queue
// per tier, the dead queue, and the legacy failed queue that is kept only so
// messages parked there before the tiers existed can still drain.
func topology(tiers []time.Duration) []queueSpec {
	var specs []queueSpec
	for _, et := range eventTypes() {
		specs = append(specs, queueSpec{name: queuePrefix + et, bindKey: et})
		for _, d := range tiers {
			specs = append(specs, queueSpec{name: tierQueueFor(et, d), args: tierQueueArgs(et, d)})
		}
		specs = append(specs,
			queueSpec{name: failedQueueFor(et), args: legacyFailedArgs(et)},
			queueSpec{name: deadQueueFor(et)},
		)
	}
	return specs
}

// declareTopology idempotently declares the exchange, all queues and the
// main-queue bindings. Every declaration is repeatable with identical
// arguments, so it is safe on every (re)connect and from every binary.
func (q *Queue) declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(exchangeMain, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %s: %w", exchangeMain, err)
	}
	for _, spec := range topology(q.tiers) {
		if _, err := ch.QueueDeclare(spec.name, true, false, false, false, spec.args); err != nil {
			return fmt.Errorf("declare queue %s: %w", spec.name, err)
		}
		if spec.bindKey != "" {
			if err := ch.QueueBind(spec.name, spec.bindKey, exchangeMain, false, nil); err != nil {
				return fmt.Errorf("bind queue %s: %w", spec.name, err)
			}
		}
	}
	return nil
}

// tierQueueFor names the delay queue of an event type and tier. The name
// carries the delay, so a changed tier list never redeclares an existing queue
// with different arguments.
func tierQueueFor(eventType string, delay time.Duration) string {
	return queuePrefix + eventType + ".delay." + tierLabel(delay)
}

// tierLabel renders a delay in the largest whole unit: 5s, 30s, 5m, 1h.
func tierLabel(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
}

// tierQueueArgs makes a delay queue expire every message after delay and
// dead-letter it to the main exchange under the event type. The TTL is a
// queue argument, so every message in the queue shares it and the oldest is
// always the first to expire: no per-message TTL, no head-of-line blocking.
func tierQueueArgs(eventType string, delay time.Duration) amqp.Table {
	return amqp.Table{
		"x-message-ttl":             delay.Milliseconds(),
		"x-dead-letter-exchange":    exchangeMain,
		"x-dead-letter-routing-key": eventType,
	}
}

// legacyFailedArgs are the arguments census.<type>.failed has always been
// declared with. They must never change: redeclaring a queue with different
// arguments fails with PRECONDITION_FAILED and would take the topology down.
func legacyFailedArgs(eventType string) amqp.Table {
	return amqp.Table{
		"x-dead-letter-exchange":    exchangeMain,
		"x-dead-letter-routing-key": eventType,
	}
}

// Publish sends a single job to the main exchange with routing key = job.Type
// and waits for broker confirmation. A successful return means the durable
// target queue accepted the message.
func (q *Queue) Publish(ctx context.Context, job contract.QueueJob) error {
	return q.publishConfirmed(ctx, exchangeMain, job.Type, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         job.Payload,
		Headers:      amqp.Table{headerAttempts: int32(0)},
	})
}

// publishConfirmed publishes one message on the shared publishing channel
// and waits for the broker confirmation. A successful return means a durable
// queue accepted the message; an unroutable mandatory publish (missing
// queue or binding) is surfaced as an error instead of being dropped.
func (q *Queue) publishConfirmed(ctx context.Context, exchange, key string, pub amqp.Publishing) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.conn.IsClosed() {
		if err := q.reconnect(); err != nil {
			return fmt.Errorf("publish reconnect: %w", err)
		}
	}

	// Drain any stale return notification from the previous publish.
	select {
	case <-q.returns:
	default:
	}

	dc, err := q.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, pub)
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if dc == nil {
		return fmt.Errorf("publish: deferred confirmation is nil")
	}

	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("publish confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("publish: broker nacked message for %q", key)
	}

	// RabbitMQ notifies returns before confirming mandatory messages.
	select {
	case ret := <-q.returns:
		return fmt.Errorf("rabbitmq unroutable: code=%d text=%q exchange=%q routing_key=%q", ret.ReplyCode, ret.ReplyText, ret.Exchange, ret.RoutingKey)
	default:
	}

	return nil
}

// Consume starts concurrency consumers for the given event types.
func (q *Queue) Consume(ctx context.Context, eventTypes []string, concurrency int, handler func(ctx context.Context, job contract.QueueJob) error) error {
	if concurrency <= 0 {
		concurrency = 4
	}

	stopClaiming, stopClaimingCancel := context.WithCancel(context.Background())
	defer stopClaimingCancel()

	go func() {
		<-ctx.Done()
		stopClaimingCancel()
	}()

	processCtx, processCancel := context.WithCancel(context.Background())
	defer processCancel()

	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			if err := q.consumeWorker(stopClaiming, processCtx, eventTypes, workerID, handler); err != nil && stopClaiming.Err() == nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("consume errors: %v", errs)
	}
	return nil
}

// ConsumeFailed drains the legacy per-event-type failed queues without
// sleeping; see failedWorker.
func (q *Queue) ConsumeFailed(ctx context.Context, types []string, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 4
	}

	var failedQueues []string
	if len(types) == 0 {
		for _, et := range eventTypes() {
			failedQueues = append(failedQueues, "census."+et+".failed")
		}
	} else {
		for _, et := range types {
			failedQueues = append(failedQueues, "census."+et+".failed")
		}
	}

	stopClaiming, stopClaimingCancel := context.WithCancel(context.Background())
	defer stopClaimingCancel()

	go func() {
		<-ctx.Done()
		stopClaimingCancel()
	}()

	processCtx, processCancel := context.WithCancel(context.Background())
	defer processCancel()

	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := range concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			if err := q.failedWorker(stopClaiming, processCtx, failedQueues, workerID); err != nil && stopClaiming.Err() == nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("failed consumer errors: %v", errs)
	}
	return nil
}

// failedWorker drains the legacy census.<type>.failed queues: messages parked
// there before the delay tiers existed. It never sleeps. Each delivery is
// moved on at once: straight to the main exchange when its old backoff has
// elapsed, into the delay tier that covers the remaining wait otherwise, or to
// the dead queue when it exhausted its attempts or has an unknown event type.
// The consumer tag carries the event type, so routing never depends on the
// delivery's routing key (default-exchange publishes carry queue-name keys).
// Every move waits for broker confirmation before the ack; nothing is
// silently dropped.
func (q *Queue) failedWorker(stopClaiming context.Context, processCtx context.Context, failedQueues []string, workerID int) error {
	ch, err := q.conn.Channel()
	if err != nil {
		return fmt.Errorf("failed worker %d channel: %w", workerID, err)
	}
	defer ch.Close()

	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("failed worker %d qos: %w", workerID, err)
	}
	if err := ch.Confirm(false); err != nil {
		return fmt.Errorf("failed worker %d confirm: %w", workerID, err)
	}

	var deliveries []<-chan amqp.Delivery
	seen := make(map[string]bool, len(failedQueues))
	for _, fq := range failedQueues {
		if seen[fq] {
			continue
		}
		seen[fq] = true
		// The consumer tag doubles as the canonical event type: every
		// redelivered message carries it, whatever routing key it arrived
		// with (legacy failures were published with prefixed keys).
		et := failedQueueEventType(fq)
		del, err := ch.Consume(fq, et, false, false, false, false, nil)
		if err != nil {
			return fmt.Errorf("failed worker %d consume %s: %w", workerID, fq, err)
		}
		deliveries = append(deliveries, del)
	}

	merged := mergeDeliveries(stopClaiming, deliveries)

	for msg := range merged {
		select {
		case <-stopClaiming.Done():
			_ = msg.Nack(false, true)
			return nil
		default:
		}

		eventType := canonicalEventType(msg.ConsumerTag)
		attempts := getAttempts(msg.Headers)

		if !knownEventType(eventType) {
			// An unknown event type cannot be re-routed: dead-park the
			// body so it stays inspectable and recoverable.
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.unknown_event",
				slog.String("event_type", eventType),
				slog.String("consumer_tag", msg.ConsumerTag),
				slog.Int("attempts", attempts),
			)
			q.ackIfDone(msg, q.parkDelivery(ch, processCtx, deadQueueFor(eventType), msg.Body, retryHeaders(msg.Headers, contract.RetryDecision{Attempts: attempts})))
			continue
		}

		route := legacyRoute(msg.Headers, q.maxAttempts, time.Now(), q.tiers)
		hdrs := retryHeaders(msg.Headers, contract.RetryDecision{Attempts: attempts})

		switch {
		case route.Dead:
			// Safety net for deliveries that exhausted the old ladder:
			// park on the durable dead queue, never discard.
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.dead_letter",
				slog.String("event_type", eventType),
				slog.Int("attempts", attempts),
			)
			q.ackIfDone(msg, q.parkDelivery(ch, processCtx, deadQueueFor(eventType), msg.Body, hdrs))
		case route.Direct:
			q.ackIfDone(msg, q.republishDirect(ch, processCtx, eventType, msg.Body, hdrs, attempts))
		default:
			target := tierQueueFor(eventType, route.Delay)
			err := q.publishToQueue(target, msg.Body, hdrs)
			if err != nil {
				q.logger.ErrorContext(
					processCtx, "rabbitmq.failed.tier_error",
					slog.String("event_type", eventType),
					slog.String("queue", target),
					slog.Any("error", err),
				)
			} else {
				q.logger.InfoContext(
					processCtx, "rabbitmq.failed.moved_to_tier",
					slog.String("event_type", eventType),
					slog.Int("attempts", attempts),
					slog.String("queue", target),
				)
			}
			q.ackIfDone(msg, err == nil)
		}
	}

	return nil
}

// ackIfDone acks the delivery when its replacement publish was confirmed and
// nacks with requeue otherwise, so a failed move never loses the message.
func (q *Queue) ackIfDone(msg amqp.Delivery, done bool) {
	if done {
		_ = msg.Ack(false)
	} else {
		_ = msg.Nack(false, true)
	}
}

// republishDirect publishes a legacy delivery to the main exchange under its
// canonical event type on the worker's confirming channel and reports whether
// the broker confirmed it.
func (q *Queue) republishDirect(ch *amqp.Channel, ctx context.Context, eventType string, body []byte, headers amqp.Table, attempts int) bool {
	dc, pubErr := ch.PublishWithDeferredConfirmWithContext(ctx, exchangeMain, eventType, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
		Headers:      headers,
	})
	if pubErr != nil {
		q.logger.ErrorContext(
			ctx, "rabbitmq.failed.republish_error",
			slog.String("event_type", eventType),
			slog.Int("attempts", attempts),
			slog.Any("error", pubErr),
		)
		return false
	}
	acked, waitErr := dc.WaitContext(ctx)
	if waitErr != nil || !acked {
		q.logger.ErrorContext(
			ctx, "rabbitmq.failed.republish_unconfirmed",
			slog.String("event_type", eventType),
			slog.Int("attempts", attempts),
			slog.Any("error", waitErr),
			slog.Bool("acked", acked),
		)
		return false
	}
	q.logger.InfoContext(
		ctx, "rabbitmq.failed.republished",
		slog.String("event_type", eventType),
		slog.Int("attempts", attempts),
	)
	return true
}

// legacyRoute decides where a message found in a legacy failed queue goes,
// from the attempt count and the not-before instant the old retry path stamped
// on it. Pure; see contract.DecideLegacyDrain.
func legacyRoute(headers amqp.Table, maxAttempts int, now time.Time, tiers []time.Duration) contract.LegacyDrain {
	return contract.DecideLegacyDrain(getAttempts(headers), maxAttempts, remainingBackoff(headers, now), tiers)
}

// parkDelivery declares the dead queue if needed and publishes the delivery
// body to it, waiting for broker confirmation. It reports whether the park
// succeeded; callers must not ack when it returns false.
func (q *Queue) parkDelivery(ch *amqp.Channel, ctx context.Context, deadQueue string, body []byte, headers amqp.Table) bool {
	if _, err := ch.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		q.logger.ErrorContext(ctx, "rabbitmq.failed.park_declare_error",
			slog.String("queue", deadQueue), slog.Any("error", err))
		return false
	}
	if err := q.publishToQueue(deadQueue, body, headers); err != nil {
		q.logger.ErrorContext(ctx, "rabbitmq.failed.park_error",
			slog.String("queue", deadQueue), slog.Any("error", err))
		return false
	}
	return true
}

// consumeWorker creates a dedicated channel and consumes from the specified
// queues. The consumer tag carries the event type; job.Type is derived from
// it so dispatch never depends on the delivery's routing key.
func (q *Queue) consumeWorker(stopClaiming context.Context, processCtx context.Context, eventTypes []string, workerID int, handler func(ctx context.Context, job contract.QueueJob) error) error {
	ch, err := q.conn.Channel()
	if err != nil {
		return fmt.Errorf("worker %d channel: %w", workerID, err)
	}
	defer ch.Close()

	if err := ch.Qos(1, 0, false); err != nil {
		return fmt.Errorf("worker %d qos: %w", workerID, err)
	}

	var deliveries []<-chan amqp.Delivery
	seen := make(map[string]bool, len(eventTypes))
	for _, et := range eventTypes {
		if seen[et] {
			continue
		}
		seen[et] = true
		queueName := queuePrefix + et
		del, err := ch.Consume(queueName, et, false, false, false, false, nil)
		if err != nil {
			return fmt.Errorf("worker %d consume %s: %w", workerID, queueName, err)
		}
		deliveries = append(deliveries, del)
	}

	merged := mergeDeliveries(stopClaiming, deliveries)

	for msg := range merged {
		select {
		case <-stopClaiming.Done():
			_ = msg.Nack(false, true)
			return nil
		default:
		}

		claimedAt := time.Now()
		job := contract.QueueJob{
			Type:    canonicalEventType(msg.ConsumerTag),
			Payload: msg.Body,
		}

		err := handler(processCtx, job)
		handlerDuration := time.Since(claimedAt)
		if err != nil {
			q.handleFailure(processCtx, msg, job.Type, err)
		} else {
			_ = msg.Ack(false)
		}

		// The unacked window is handler time plus publish/confirm time.
		// Anything past the threshold shows up here so delayed acks stay
		// visible cluster-wide instead of silently inflating redelivery
		// risk.
		if elapsed := time.Since(claimedAt); elapsed > slowAckThreshold {
			q.logger.WarnContext(
				processCtx, "queue.slow_ack",
				slog.String("event_type", job.Type),
				slog.Int("attempts", getAttempts(msg.Headers)),
				slog.Duration("handler_duration", handlerDuration),
				slog.Duration("claim_to_ack", elapsed),
			)
		}
	}

	return nil
}

// handleFailure routes a failed delivery according to contract.DecideRetry.
// Poison payloads, no-handler deliveries and deliveries that exhausted their
// attempts dead-park on the event's dead queue. Everything else is published
// to the delay tier the decision picked, whose queue-level TTL dead-letters it
// back to the main queue when the delay is over. A proxy-attributable error
// leaves the attempt count unchanged. The delivery is acked only after the
// replacement publish was confirmed; a failed publish nacks with requeue so no
// message is ever dropped. jobType is the normalized event type from the
// consumer, never the delivery's routing key.
func (q *Queue) handleFailure(ctx context.Context, msg amqp.Delivery, jobType string, handlerErr error) {
	decision := contract.DecideRetry(handlerErr, getAttempts(msg.Headers), q.maxAttempts, q.tiers)
	hdrs := retryHeaders(msg.Headers, decision)

	if decision.Dead {
		deadQueue := deadQueueFor(jobType)
		q.logger.ErrorContext(
			ctx, "rabbitmq.dead_park",
			slog.String("event_type", jobType),
			slog.Int("attempts", decision.Attempts),
			slog.String("queue", deadQueue),
			slog.Any("error", handlerErr),
		)
		if err := q.publishToQueue(deadQueue, msg.Body, hdrs); err != nil {
			q.logger.ErrorContext(ctx, "rabbitmq.dead_park_error", slog.String("event_type", jobType), slog.Any("error", err))
			_ = msg.Nack(false, true)
			return
		}
		_ = msg.Ack(false)
		return
	}

	target := tierQueueFor(jobType, decision.Delay)
	q.logger.ErrorContext(
		ctx, "rabbitmq.retry",
		slog.String("event_type", jobType),
		slog.Int("attempts", decision.Attempts),
		slog.Bool("proxy_fault", decision.ProxyFault),
		slog.Int("proxy_failures", getProxyFailures(hdrs)),
		slog.String("queue", target),
		slog.Any("error", handlerErr),
	)
	if err := q.publishToQueue(target, msg.Body, hdrs); err != nil {
		q.logger.ErrorContext(ctx, "rabbitmq.retry_publish_error", slog.String("event_type", jobType), slog.Any("error", err))
		_ = msg.Nack(false, true)
		return
	}

	_ = msg.Ack(false)
}

// publishToQueue publishes a persistent body to a side queue (delay or dead)
// through the default exchange and waits for broker confirmation. The queue's
// own arguments decide expiry; no per-message TTL is set.
func (q *Queue) publishToQueue(queueName string, body []byte, headers amqp.Table) error {
	return q.publishConfirmed(context.Background(), "", queueName, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
		Headers:      headers,
	})
}

// Close closes the publishing channel and then the AMQP connection.
func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	var chErr, connErr error
	if q.ch != nil {
		chErr = q.ch.Close()
	}
	if q.conn != nil && !q.conn.IsClosed() {
		connErr = q.conn.Close()
	}
	return errors.Join(chErr, connErr)
}

// reconnect re-establishes the AMQP connection, channel, and confirm/return handling.
func (q *Queue) reconnect() error {
	conn, ch, rets, err := q.openSession()
	if err != nil {
		return err
	}
	q.conn = conn
	q.ch = ch
	q.returns = rets
	return nil
}

// getAttempts extracts the attempt count from message headers.
func getAttempts(headers amqp.Table) int {
	if headers == nil {
		return 0
	}
	if v, ok := headers[headerAttempts]; ok {
		if n, ok := v.(int32); ok {
			return int(n)
		}
	}
	return 0
}

// remainingBackoff reports how long a failed-queue delivery must still wait
// before it may be republished to the main queue. Missing, malformed, or
// already-elapsed x-not-before-ms headers mean no wait.
func remainingBackoff(headers amqp.Table, now time.Time) time.Duration {
	if headers == nil {
		return 0
	}
	v, ok := headers[headerNotBefore]
	if !ok {
		return 0
	}
	ms, ok := v.(int64)
	if !ok {
		return 0
	}
	notBefore := time.UnixMilli(ms)
	if !notBefore.After(now) {
		return 0
	}
	return notBefore.Sub(now)
}

// getProxyFailures extracts the proxy-failure count from message headers.
func getProxyFailures(headers amqp.Table) int {
	if v, ok := headers[headerProxyFailures]; ok {
		if n, ok := v.(int32); ok {
			return int(n)
		}
	}
	return 0
}

// brokerHeaders are set by RabbitMQ itself while dead-lettering. They are not
// republished: the broker rebuilds them, and echoing them back only grows the
// header table on every pass through a delay tier. headerNotBefore belongs to
// the pre-tier path and is meaningless after the move.
var brokerHeaders = map[string]bool{
	"x-death":                true,
	"x-first-death-exchange": true,
	"x-first-death-queue":    true,
	"x-first-death-reason":   true,
	"x-last-death-exchange":  true,
	"x-last-death-queue":     true,
	"x-last-death-reason":    true,
	headerNotBefore:          true,
}

// retryHeaders builds the header table of a message about to be retried or
// parked: the decision's attempt count, the proxy-failure count (raised by one
// for a proxy-attributable failure), and the previous headers minus broker
// bookkeeping. The input table is never modified.
func retryHeaders(prev amqp.Table, d contract.RetryDecision) amqp.Table {
	proxyFailures := getProxyFailures(prev)
	if d.ProxyFault {
		proxyFailures++
	}
	out := amqp.Table{headerAttempts: int32(d.Attempts)}
	if proxyFailures > 0 {
		out[headerProxyFailures] = int32(proxyFailures)
	}
	for k, v := range prev {
		if k == headerAttempts || k == headerProxyFailures || brokerHeaders[k] {
			continue
		}
		out[k] = v
	}
	return out
}

// eventTypes returns the known event type strings used for queue naming.
func eventTypes() []string {
	return []string{
		"id-sweep",
		"character-census",
		"achievement-census",
		"new-proxy",
	}
}

// knownEventType reports whether et is a registered event type.
func knownEventType(et string) bool {
	for _, known := range eventTypes() {
		if et == known {
			return true
		}
	}
	return false
}

// canonicalEventType strips a trailing ".failed" suffix, normalizing a
// queue-derived or consumer-tag event name to the main event type
// ("id-sweep.failed" and "id-sweep" both map to "id-sweep").
func canonicalEventType(et string) string {
	return strings.TrimSuffix(et, ".failed")
}

// failedQueueEventType extracts the canonical event type from a failed
// queue name ("census.id-sweep.failed" → "id-sweep").
func failedQueueEventType(failedQueue string) string {
	return canonicalEventType(strings.TrimPrefix(failedQueue, queuePrefix))
}

// failedQueueFor returns the per-event-type failed queue name.
func failedQueueFor(eventType string) string {
	return queuePrefix + eventType + ".failed"
}

// deadQueueFor returns the per-event-type dead queue name.
func deadQueueFor(eventType string) string {
	return queuePrefix + eventType + ".dead"
}

// mergeDeliveries merges multiple amqp.Delivery channels into one.
func mergeDeliveries(ctx context.Context, channels []<-chan amqp.Delivery) <-chan amqp.Delivery {
	merged := make(chan amqp.Delivery)
	var wg sync.WaitGroup

	for _, ch := range channels {
		wg.Add(1)
		go func(c <-chan amqp.Delivery) {
			defer wg.Done()
			for {
				select {
				case msg, ok := <-c:
					if !ok {
						return
					}
					select {
					case merged <- msg:
					case <-ctx.Done():
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}(ch)
	}

	go func() {
		wg.Wait()
		close(merged)
	}()

	return merged
}

// Ensure Queue implements contract.Queue at compile time.
var _ contract.Queue = (*Queue)(nil)
