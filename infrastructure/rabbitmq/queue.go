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
	headerNotBefore  = "x-not-before-ms"
	backoffBaseSec   = 5
	maxBackoffSec    = 3600
	slowAckThreshold = 30 * time.Second
)

// Queue is a RabbitMQ-backed work queue implementing contract.Queue.
type Queue struct {
	url         string
	conn        *amqp.Connection
	ch          *amqp.Channel
	returns     <-chan amqp.Return
	mu          sync.Mutex
	logger      contract.Logger
	maxAttempts int
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
// returns a ready-to-use Queue. maxAttempts is the failed-worker safety
// net: a delivery that reaches a failed queue with attempts >= maxAttempts
// is dead-parked instead of republished; it must be at least 1.
func New(url string, logger contract.Logger, maxAttempts int) (*Queue, error) {
	if maxAttempts < 1 {
		return nil, fmt.Errorf("rabbitmq max attempts must be >= 1, got %d", maxAttempts)
	}
	q := &Queue{url: url, logger: logger, maxAttempts: maxAttempts}
	conn, ch, rets, err := q.openSession()
	if err != nil {
		return nil, err
	}
	q.conn = conn
	q.ch = ch
	q.returns = rets
	return q, nil
}

// declareTopology idempotently declares all exchanges, queues, and bindings.
func (q *Queue) declareTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(exchangeMain, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %s: %w", exchangeMain, err)
	}

	for _, et := range eventTypes() {
		mainQueue := "census." + et
		failedQueue := mainQueue + ".failed"

		if _, err := ch.QueueDeclare(mainQueue, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %s: %w", mainQueue, err)
		}
		if err := ch.QueueBind(mainQueue, et, exchangeMain, false, nil); err != nil {
			return fmt.Errorf("bind queue %s: %w", mainQueue, err)
		}

		failedArgs := amqp.Table{
			"x-dead-letter-exchange":    exchangeMain,
			"x-dead-letter-routing-key": et,
		}
		if _, err := ch.QueueDeclare(failedQueue, true, false, false, false, failedArgs); err != nil {
			return fmt.Errorf("declare queue %s: %w", failedQueue, err)
		}

		deadQueue := mainQueue + ".dead"
		if _, err := ch.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare queue %s: %w", deadQueue, err)
		}
	}

	return nil
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

// ConsumeFailed consumes from per-event-type failed queues and re-publishes
// messages back to the main exchange.
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

// failedWorker consumes from failed queues and re-publishes to the main
// exchange under the canonical event type. The consumer tag carries the
// event type, so routing never depends on the delivery's routing key
// (default-exchange publishes carry queue-name keys). Republishes wait for
// broker confirmation before acking; exhausted or unclassifiable deliveries
// dead-park on an inspectable dead queue — nothing is silently dropped.
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
		attempts := getAttempts(msg.Headers) + 1

		if !knownEventType(eventType) {
			// An unknown event type cannot be re-routed: dead-park the
			// body so it stays inspectable and recoverable.
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.unknown_event",
				slog.String("event_type", eventType),
				slog.String("consumer_tag", msg.ConsumerTag),
				slog.Int("attempts", attempts),
			)
			if q.parkDelivery(ch, processCtx, queuePrefix+eventType+".dead", msg.Body, msg.Headers, attempts) {
				_ = msg.Ack(false)
			} else {
				_ = msg.Nack(false, true)
			}
			continue
		}

		q.logger.DebugContext(
			processCtx, "rabbitmq.failed.received",
			slog.String("event_type", eventType),
			slog.Int("attempts", attempts),
			slog.String("queue", msg.ConsumerTag),
		)

		if attempts >= q.maxAttempts {
			// Safety net for deliveries that exhaust the failed-queue
			// ladder: park on the durable dead queue, never discard.
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.dead_letter",
				slog.String("event_type", eventType),
				slog.Int("attempts", attempts),
			)
			if q.parkDelivery(ch, processCtx, deadQueueFor(eventType), msg.Body, msg.Headers, attempts) {
				_ = msg.Ack(false)
			} else {
				_ = msg.Nack(false, true)
			}
			continue
		}

		// Honor the retry ladder: hold the delivery until the not-before
		// instant set when the message entered the failed queue. Without
		// this wait a live failed-queue consumer would bypass the per-
		// message TTL and re-fire failures at full broker speed.
		if wait := remainingBackoff(msg.Headers, time.Now()); wait > 0 {
			q.logger.DebugContext(
				processCtx, "rabbitmq.failed.backoff_wait",
				slog.String("event_type", eventType),
				slog.Int("attempts", attempts),
				slog.Duration("wait", wait),
			)
			timer := time.NewTimer(wait)
			select {
			case <-stopClaiming.Done():
				timer.Stop()
				_ = msg.Nack(false, true)
				return nil
			case <-processCtx.Done():
				timer.Stop()
				_ = msg.Nack(false, true)
				return nil
			case <-timer.C:
			}
		}

		dc, pubErr := ch.PublishWithDeferredConfirmWithContext(processCtx, exchangeMain, eventType, false, false, amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			Body:         msg.Body,
			Headers:      copyHeaders(msg.Headers, attempts),
		})
		if pubErr != nil {
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.republish_error",
				slog.String("event_type", eventType),
				slog.Int("attempts", attempts),
				slog.Any("error", pubErr),
			)
			_ = msg.Nack(false, true)
			continue
		}
		acked, waitErr := dc.WaitContext(processCtx)
		if waitErr != nil || !acked {
			q.logger.ErrorContext(
				processCtx, "rabbitmq.failed.republish_unconfirmed",
				slog.String("event_type", eventType),
				slog.Int("attempts", attempts),
				slog.Any("error", waitErr),
				slog.Bool("acked", acked),
			)
			_ = msg.Nack(false, true)
			continue
		}

		_ = msg.Ack(false)
		q.logger.InfoContext(
			processCtx, "rabbitmq.failed.republished",
			slog.String("event_type", eventType),
			slog.Int("attempts", attempts),
		)
	}

	return nil
}

// parkDelivery declares the dead queue if needed and publishes the delivery
// body to it, waiting for broker confirmation. It reports whether the park
// succeeded; callers must not ack when it returns false.
func (q *Queue) parkDelivery(ch *amqp.Channel, ctx context.Context, deadQueue string, body []byte, headers amqp.Table, attempts int) bool {
	if _, err := ch.QueueDeclare(deadQueue, true, false, false, false, nil); err != nil {
		q.logger.ErrorContext(ctx, "rabbitmq.failed.park_declare_error",
			slog.String("queue", deadQueue), slog.Any("error", err))
		return false
	}
	if err := q.publishToFailed(deadQueue, body, headers, attempts, 0); err != nil {
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

// retryBackoffSec returns the exponential backoff in seconds for the Nth
// failed attempt: 5, 10, 20, 40, ... capped at one hour. Retryable failures
// are retried forever, so there is no permanent outcome on this ladder.
func retryBackoffSec(attempts int) int {
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	// 5s * 2^shift saturates the 3600s cap from shift 10 onwards; clamping
	// early keeps the shift well inside every int width.
	if shift > 10 {
		return maxBackoffSec
	}
	backoff := backoffBaseSec * (1 << shift)
	if backoff > maxBackoffSec {
		return maxBackoffSec
	}
	return backoff
}

// failureOutcome is the classified fate of a failed delivery: dead-park on
// the event's dead queue, or requeue onto the failed queue with a backoff.
type failureOutcome struct {
	dead       bool
	backoffSec int
}

// failureDecision classifies a failed delivery. Poison payloads and
// unrouteable event types dead-park immediately: retrying them can never
// succeed and would churn the cluster forever. Everything else —
// challenges, timeouts, 429s, 5xx, connection errors — retries on the
// exponential ladder until a handler ends it with a terminal outcome
// (genuine Lodestone 404/403 are acked by the handler itself).
func failureDecision(handlerErr error, attempts int) failureOutcome {
	if errors.Is(handlerErr, contract.ErrPoisonPayload) || errors.Is(handlerErr, contract.ErrNoHandler) {
		return failureOutcome{dead: true}
	}
	return failureOutcome{backoffSec: retryBackoffSec(attempts)}
}

// handleFailure requeues a failed delivery according to failureDecision:
// poison payloads and no-handler deliveries dead-park on the event's dead
// queue, everything else enters the per-event-type failed queue with the
// attempt-incremented backoff ladder. The delivery is acked only after the
// replacement publish was confirmed; a failed publish nacks with requeue so
// no message is ever dropped. jobType is the normalized event type from the
// consumer, never the delivery's routing key.
func (q *Queue) handleFailure(ctx context.Context, msg amqp.Delivery, jobType string, handlerErr error) {
	attempts := getAttempts(msg.Headers) + 1
	decision := failureDecision(handlerErr, attempts)

	if decision.dead {
		deadQueue := deadQueueFor(jobType)
		q.logger.ErrorContext(
			ctx, "rabbitmq.dead_park",
			slog.String("event_type", jobType),
			slog.Int("attempts", attempts),
			slog.String("queue", deadQueue),
			slog.Any("error", handlerErr),
		)
		if err := q.publishToFailed(deadQueue, msg.Body, msg.Headers, attempts, 0); err != nil {
			q.logger.ErrorContext(ctx, "rabbitmq.dead_park_error", slog.String("event_type", jobType), slog.Any("error", err))
			_ = msg.Nack(false, true)
			return
		}
		_ = msg.Ack(false)
		return
	}

	q.logger.ErrorContext(
		ctx, "rabbitmq.retry",
		slog.String("event_type", jobType),
		slog.Int("attempts", attempts),
		slog.Int("backoff_sec", decision.backoffSec),
		slog.Any("error", handlerErr),
	)
	if err := q.publishToFailed(failedQueueFor(jobType), msg.Body, msg.Headers, attempts, decision.backoffSec); err != nil {
		q.logger.ErrorContext(ctx, "rabbitmq.retry_publish_error", slog.String("event_type", jobType), slog.Any("error", err))
		_ = msg.Nack(false, true)
		return
	}

	_ = msg.Ack(false)
}

// publishToFailed publishes a persistent body to a side queue (failed or
// dead) through the default exchange and waits for broker confirmation.
// The header table is rebuilt with the incremented attempt count.
func (q *Queue) publishToFailed(queueName string, body []byte, headers amqp.Table, attempts, backoffSec int) error {
	hdrs := copyHeaders(headers, attempts)
	if backoffSec > 0 {
		// The failedWorker sleeps until this instant before republishing;
		// deployments without a failed-queue consumer rely on the matching
		// per-message Expiration plus the queue's dead-letter exchange.
		hdrs[headerNotBefore] = time.Now().Add(time.Duration(backoffSec) * time.Second).UnixMilli()
	}
	pub := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
		Headers:      hdrs,
	}
	if backoffSec > 0 {
		pub.Expiration = fmt.Sprintf("%d000", backoffSec)
	}
	return q.publishConfirmed(context.Background(), "", queueName, pub)
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

// copyHeaders creates a new headers table with updated attempt count.
func copyHeaders(headers amqp.Table, attempts int) amqp.Table {
	new := amqp.Table{headerAttempts: int32(attempts)}
	if headers != nil {
		for k, v := range headers {
			if k != headerAttempts {
				new[k] = v
			}
		}
	}
	return new
}

// eventTypes returns the known event type strings used for queue naming.
func eventTypes() []string {
	return []string{
		"id-sweep",
		"character-census",
		"achievement-census",
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
