package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"snap-flow-go/internal/logging"
	"sync"
	"time"

	"snap-flow-go/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

var ErrUnroutable = errors.New("message was returned as unroutable")

type RabbitPublisher struct {
	cfg      config.RabbitMQConfig
	gate     chan struct{}
	conn     *amqp.Connection
	channel  *amqp.Channel
	confirms chan amqp.Confirmation
	returns  chan amqp.Return
}

func NewRabbitPublisher(cfg config.RabbitMQConfig) (*RabbitPublisher, error) {
	if cfg.ParkingReplayIntervalSeconds == 0 {
		cfg.ParkingReplayIntervalSeconds = 60
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9._-]{1,80}$`).MatchString(cfg.QueuePrefix) ||
		cfg.Prefetch < 1 || cfg.Prefetch > 32 || len(cfg.RetryDelaysMS) < 1 || len(cfg.RetryDelaysMS) > 19 ||
		cfg.RetryDelayMS < 1 || cfg.RetryDelayMS > 2147483647 || cfg.PublishTimeoutMS < 1 || cfg.PublishTimeoutMS > 60000 || cfg.ParkingReplayIntervalSeconds < 1 || cfg.ParkingReplayIntervalSeconds > 86400 {
		return nil, errors.New("invalid RabbitMQ queue/retry configuration")
	}
	for _, delay := range cfg.RetryDelaysMS {
		if delay < 1 || delay > 2147483647 {
			return nil, errors.New("invalid RabbitMQ retry delay")
		}
	}
	cfg.RetryDelaysMS = append([]int(nil), cfg.RetryDelaysMS...)
	p := &RabbitPublisher{cfg: cfg, gate: make(chan struct{}, 1)}
	p.gate <- struct{}{}
	return p, nil
}

func (p *RabbitPublisher) MaxAttempts() int { return len(p.cfg.RetryDelaysMS) + 1 }

func retryQueue(prefix string, index int) string { return fmt.Sprintf("%s.retry.%d", prefix, index+1) }

func brokerURI(cfg config.RabbitMQConfig) string {
	u := &url.URL{Scheme: "amqp", User: url.UserPassword(cfg.User, cfg.Password), Host: net.JoinHostPort(cfg.Host, cfg.Port), Path: "/" + cfg.VirtualHost, RawPath: "/" + url.PathEscape(cfg.VirtualHost)}
	return u.String()
}

type boundedConn struct {
	net.Conn
	timeout time.Duration
}

func (c *boundedConn) Write(data []byte) (int, error) {
	if err := c.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(data)
}

func dialBroker(cfg config.RabbitMQConfig, timeout time.Duration) (*amqp.Connection, error) {
	var socket net.Conn
	conn, err := amqp.DialConfig(brokerURI(cfg), amqp.Config{Heartbeat: 5 * time.Second, Locale: "en_US", Dial: func(network, address string) (net.Conn, error) {
		raw, err := net.DialTimeout(network, address, timeout)
		if err != nil {
			return nil, err
		}
		socket = raw
		if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
			_ = raw.Close() // Best effort while returning the deadline error.
			return nil, err
		}
		return &boundedConn{Conn: raw, timeout: timeout}, nil
	}})
	if err == nil && socket != nil {
		_ = socket.SetReadDeadline(time.Time{})
	}
	if err != nil {
		return nil, errors.New("RabbitMQ connection failed; check broker readiness and credentials")
	}
	return conn, nil
}

func declareQueues(ch *amqp.Channel, cfg config.RabbitMQConfig) error {
	for _, suffix := range []string{"tasks", "parking", "retry"} {
		args := amqp.Table{"x-queue-type": "quorum", "x-overflow": "reject-publish"}
		if suffix == "tasks" {
			args["x-delivery-limit"] = int32(20)
			args["x-dead-letter-exchange"] = ""
			args["x-dead-letter-routing-key"] = cfg.QueuePrefix + ".parking"
			args["x-dead-letter-strategy"] = "at-least-once"
		}
		if suffix == "retry" {
			args["x-message-ttl"] = int32(cfg.RetryDelayMS) // #nosec G115 -- NewRabbitPublisher validates the int32 range.
			args["x-dead-letter-exchange"] = ""
			args["x-dead-letter-routing-key"] = cfg.QueuePrefix + ".tasks"
			args["x-dead-letter-strategy"] = "at-least-once"
		}
		if _, err := ch.QueueDeclare(cfg.QueuePrefix+"."+suffix, true, false, false, false, args); err != nil {
			return err
		}
	}
	for index, delay := range cfg.RetryDelaysMS {
		args := amqp.Table{"x-queue-type": "quorum", "x-overflow": "reject-publish", "x-message-ttl": int32(delay), // #nosec G115 -- delays are validated before publisher construction.
			"x-dead-letter-exchange": "", "x-dead-letter-routing-key": cfg.QueuePrefix + ".tasks", "x-dead-letter-strategy": "at-least-once"}
		if _, err := ch.QueueDeclare(retryQueue(cfg.QueuePrefix, index), true, false, false, false, args); err != nil {
			return err
		}
	}
	return nil
}

func (p *RabbitPublisher) reset() {
	if p.conn != nil {
		_ = p.conn.CloseDeadline(time.Now().Add(time.Second))
	}
	p.conn, p.channel = nil, nil
}

func (p *RabbitPublisher) Close() error {
	<-p.gate
	defer func() { p.gate <- struct{}{} }()
	p.reset()
	return nil
}

func (p *RabbitPublisher) open(ctx context.Context) error {
	if p.conn != nil && !p.conn.IsClosed() && p.channel != nil && !p.channel.IsClosed() {
		return nil
	}
	p.reset()
	timeout := time.Duration(p.cfg.PublishTimeoutMS) * time.Millisecond
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}
	conn, err := dialBroker(p.cfg, timeout)
	if err != nil {
		return fmt.Errorf("RabbitMQ connection failed: %w", err)
	}
	p.conn = conn
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseDeadline(time.Now()) })
	defer stop()
	ch, err := conn.Channel()
	if err != nil {
		p.reset()
		return err
	}
	p.channel = ch
	if err := declareQueues(ch, p.cfg); err != nil {
		p.reset()
		return err
	}
	if err := ch.Confirm(false); err != nil {
		p.reset()
		return err
	}
	p.confirms = ch.NotifyPublish(make(chan amqp.Confirmation, 1))
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 1))
	return nil
}

func (p *RabbitPublisher) Publish(ctx context.Context, task Task) error {
	if err := validateTask(task); err != nil {
		return err
	}
	body, err := json.Marshal(task)
	if err != nil {
		return err
	}
	if len(body) > MaxMessageBytes {
		return errors.New("task exceeds 64 KiB limit")
	}
	return p.publishRaw(ctx, p.cfg.QueuePrefix+".tasks", body, nil)
}

func (p *RabbitPublisher) PublishFailure(ctx context.Context, body []byte, reason string) error {
	return p.publishRaw(ctx, p.cfg.QueuePrefix+".parking", body, amqp.Table{"x-reason": reason})
}

// One in-flight publication per channel keeps confirms/returns unambiguous.
// On timeout its outcome is unknown: close the channel, never reuse stale acks.
func (p *RabbitPublisher) publishRaw(ctx context.Context, routingKey string, body []byte, headers amqp.Table) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.PublishTimeoutMS)*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	if err := p.open(ctx); err != nil {
		return err
	}
	conn := p.conn
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseDeadline(time.Now()) })
	defer stop()
	var envelope Task
	_ = json.Unmarshal(body, &envelope)
	if headers == nil {
		headers = amqp.Table{}
	}
	headers["request_id"] = envelope.RequestID
	err := p.channel.PublishWithContext(ctx, "", routingKey, true, false, amqp.Publishing{MessageId: envelope.ID, ContentType: "application/json", DeliveryMode: amqp.Persistent, Timestamp: time.Now(), Body: body, Headers: headers})
	if err != nil {
		p.reset()
		return err
	}
	var returned bool
	for {
		select {
		case _, ok := <-p.returns:
			if !ok {
				p.reset()
				return errors.New("RabbitMQ channel closed before confirmation")
			}
			returned = true
		case confirm, ok := <-p.confirms:
			// AMQP orders basic.return before basic.ack. Both listeners are
			// buffered, so drain a return if select chose the ack first.
			select {
			case _, open := <-p.returns:
				returned = returned || open
			default:
			}
			if returned {
				p.reset()
				return ErrUnroutable
			}
			if !ok || !confirm.Ack {
				p.reset()
				return errors.New("RabbitMQ did not confirm publication")
			}
			return nil
		case <-ctx.Done():
			p.reset()
			return ctx.Err()
		}
	}
}

type Handler func(context.Context, Task) error

func decodeTask(body []byte) (Task, error) {
	var task Task
	if len(body) > MaxMessageBytes {
		return task, errors.New("message exceeds application limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil {
		return task, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return task, errors.New("trailing message content")
	}
	return task, validateTask(task)
}

func callHandler(ctx context.Context, handle Handler, task Task) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("task handler panicked")
		}
	}()
	return handle(ctx, task)
}

func handleDelivery(ctx context.Context, p *RabbitPublisher, handle Handler, delivery amqp.Delivery) error {
	task, err := decodeTask(delivery.Body)
	if err != nil {
		// Preserve malformed internal messages while applying the same retry
		// schedule; their bodies cannot carry an attempt counter.
		attempt := 0
		if value, ok := delivery.Headers["x-snapflow-attempt"].(int32); ok && value > 0 {
			attempt = int(value)
		}
		headers := amqp.Table{}
		for key, value := range delivery.Headers {
			headers[key] = value
		}
		headers["x-reason"] = "invalid_message"
		route := p.cfg.QueuePrefix + ".parking"
		if attempt < len(p.cfg.RetryDelaysMS) {
			route = retryQueue(p.cfg.QueuePrefix, attempt)
			headers["x-snapflow-attempt"] = int32(attempt + 1) // #nosec G115 -- retry schedules are limited to 19 entries.
		}
		if err := p.publishRaw(ctx, route, delivery.Body, headers); err != nil {
			return err
		}
		return delivery.Ack(false)
	}
	if err == nil && task.Attempt >= p.MaxAttempts() {
		err = permanent(errors.New("attempt limit already exceeded"))
	}
	if err == nil {
		ctx = logging.WithRequestID(ctx, task.RequestID)
		logging.Event(ctx, "INFO", "task_started", "task_id", task.ID, "task_type", task.Type, "attempt", task.Attempt)
		err = callHandler(ctx, handle, task)
		if err == nil {
			logging.Event(ctx, "INFO", "task_complete", "task_id", task.ID)
		} else {
			logging.Event(ctx, "WARNING", "task_failed", "task_id", task.ID, "error_code", "handler_failed")
		}
		if err == nil && ctx.Err() == nil {
			return delivery.Ack(false)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrFailurePersistence) {
		// If PostgreSQL is unavailable, retain the task until its terminal status
		// can be recorded. Never hot-loop unacked deliveries into broker DLQ.
		if e := p.publishRaw(ctx, retryQueue(p.cfg.QueuePrefix, len(p.cfg.RetryDelaysMS)-1), delivery.Body, amqp.Table{"x-reason": "failure_persistence"}); e != nil {
			return e
		}
		return delivery.Ack(false)
	}
	if errors.Is(err, ErrInvalidImage) {
		// The failure status and cleanup request were persisted by the handler.
		return delivery.Ack(false)
	}
	route := p.cfg.QueuePrefix + ".parking"
	body := delivery.Body
	reason := "invalid_message"
	if validateTask(task) == nil && !errors.Is(err, ErrTerminalRecorded) && task.Attempt < len(p.cfg.RetryDelaysMS) {
		route = retryQueue(p.cfg.QueuePrefix, task.Attempt)
		task.Attempt++
		body, _ = json.Marshal(task)
		reason = "retry"
	} else if validateTask(task) == nil {
		reason = "failed_or_exhausted"
	}
	if err := p.publishRaw(ctx, route, body, amqp.Table{"x-reason": reason}); err != nil {
		// Leave the original unacked. Session closure requeues it, followed
		// by reconnect backoff rather than an immediate hot requeue loop.
		return err
	}
	return delivery.Ack(false)
}

func RunWorker(ctx context.Context, publisher *RabbitPublisher, handle Handler) error {
	for ctx.Err() == nil {
		if err := consumeSession(ctx, publisher, handle); err != nil && ctx.Err() == nil {
			logging.Event(ctx, "WARNING", "consumer_reconnecting", "error_code", "connection_lost")
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func consumeSession(ctx context.Context, publisher *RabbitPublisher, handle Handler) error {
	cfg := publisher.cfg
	conn, err := dialBroker(cfg, time.Duration(cfg.PublishTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) }() // Best effort on session teardown.
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseDeadline(time.Now()) })
	defer stop()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	if err := declareQueues(ch, cfg); err != nil {
		return err
	}
	if err := ch.Qos(cfg.Prefetch, 0, false); err != nil {
		return err
	}
	closed := ch.NotifyClose(make(chan *amqp.Error, 1))
	deliveries, err := ch.Consume(cfg.QueuePrefix+".tasks", "snapflow-worker", false, false, false, false, nil)
	if err != nil {
		return err
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, 1)
	var workers sync.WaitGroup
	for i := 0; i < cfg.Prefetch; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-sessionCtx.Done():
					return
				case delivery, ok := <-deliveries:
					if !ok {
						select {
						case failures <- errors.New("consumer subscription closed"):
						default:
						}
						return
					}
					if err := handleDelivery(sessionCtx, publisher, handle, delivery); err != nil {
						select {
						case failures <- err:
						default:
						}
						return
					}
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case failure := <-closed:
		err = fmt.Errorf("consumer channel closed: %v", failure)
	case err = <-failures:
	}
	cancel()
	// Closing the connection returns every unacknowledged delivery to the
	// durable queue, including an uncertain retry/parking transfer.
	_ = conn.CloseDeadline(time.Now().Add(time.Second))
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	return err
}
