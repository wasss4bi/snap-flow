package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"snap-flow-go/internal/config"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestTaskContractAndSize(t *testing.T) {
	task, err := NewImageProcessTask("photo.jpg", "photo.jpg", 7)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(task)
	if err != nil || len(body) > MaxMessageBytes {
		t.Fatalf("10 MiB image does not fit task limit: %d %v", len(body), err)
	}
	decoded, err := decodeTask(body)
	if err != nil || decoded.ID != task.ID || decoded.Attempt != 0 {
		t.Fatalf("task round trip failed: %v", err)
	}
	for _, body := range [][]byte{[]byte(`{`), []byte(`{"version":99,"id":"x","type":"image:delete","attempt":0,"payload":{}}`), []byte(`{"version":2,"id":"x","type":"unknown","attempt":0,"payload":{}}`), append(body, []byte(` {}`)...)} {
		if _, err := decodeTask(body); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}

func TestUnavailableBrokerIsBounded(t *testing.T) {
	cfg := config.Load().RabbitMQ
	cfg.Host, cfg.Port, cfg.PublishTimeoutMS = "127.0.0.1", "1", 100
	publisher, err := NewRabbitPublisher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	task, _ := NewImageDeleteTask("photo.jpg")
	started := time.Now()
	if err := publisher.Publish(context.Background(), task); err == nil {
		t.Fatal("unavailable broker accepted publication")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("publication was not bounded")
	}
}

func TestRabbitDeliveryRetriesAndReconnect(t *testing.T) {
	if os.Getenv("RUN_RABBIT_TESTS") != "1" {
		t.Skip("requires isolated RabbitMQ; use Compose tests service")
	}
	cfg := config.Load().RabbitMQ
	cfg.QueuePrefix = "snapflow.test." + uuid.NewString()
	cfg.Prefetch, cfg.MaxAttempts, cfg.RetryDelayMS = 1, 3, 150
	publisher, err := NewRabbitPublisher(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	conn, err := dialBroker(cfg, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := declareQueues(ch, cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"tasks", "retry", "parking"} {
			_, _ = ch.QueueDelete(cfg.QueuePrefix+"."+name, false, false, false)
		}
	})
	wait := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timeout: %s", label)
	}
	parking := func() amqp.Delivery {
		t.Helper()
		var found amqp.Delivery
		wait("parking delivery", func() bool {
			msg, ok, err := ch.Get(cfg.QueuePrefix+".parking", false)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				found = msg
				if err := msg.Ack(false); err != nil {
					t.Fatal(err)
				}
				return true
			}
			return false
		})
		return found
	}
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = RunWorker(ctx, publisher, func(context.Context, Task) error { calls.Add(1); return errors.New("temporary storage failure") })
	}()
	task, _ := NewImageDeleteTask("retry.jpg")
	started := time.Now()
	if err := publisher.Publish(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	msg := parking()
	var parked Task
	if err := json.Unmarshal(msg.Body, &parked); err != nil || parked.ID != task.ID || parked.Attempt != 2 || calls.Load() != 3 {
		t.Fatalf("retry contract: attempts=%d calls=%d err=%v", parked.Attempt, calls.Load(), err)
	}
	if time.Since(started) < 2*time.Duration(cfg.RetryDelayMS)*time.Millisecond {
		t.Fatal("retry delay was skipped")
	}
	before := calls.Load()
	for _, malformed := range [][]byte{[]byte(`not json`), []byte(`{"version":99,"id":"bad","type":"image:delete","attempt":0,"payload":{}}`)} {
		if err := publisher.publishRaw(context.Background(), cfg.QueuePrefix+".tasks", malformed, nil); err != nil {
			t.Fatal(err)
		}
		got := parking()
		if !bytes.Equal(got.Body, malformed) || calls.Load() != before {
			t.Fatal("malformed message was executed/re-written")
		}
	}
	if err := publisher.publishRaw(context.Background(), cfg.QueuePrefix+".missing", []byte(`{}`), nil); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("mandatory return not handled: %v", err)
	}
	// A new publish must recover after the previous channel was discarded.
	if err := publisher.Publish(context.Background(), task); err != nil {
		t.Fatalf("publisher did not reconnect: %v", err)
	}
	_ = parking()
	cancel()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("worker failed to stop")
	}

	// Interrupt a consumer before ack; the same durable task must be delivered
	// to the next session. Its first handler deliberately waits for cancellation.
	ctx1, stop1 := context.WithCancel(context.Background())
	t.Cleanup(stop1)
	startedDelivery := make(chan struct{}, 1)
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		_ = RunWorker(ctx1, publisher, func(ctx context.Context, task Task) error {
			startedDelivery <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	if err := publisher.Publish(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startedDelivery:
	case <-time.After(10 * time.Second):
		t.Fatal("first delivery missing")
	}
	stop1()
	select {
	case <-done1:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted consumer did not exit")
	}
	ctx2, stop2 := context.WithCancel(context.Background())
	t.Cleanup(stop2)
	done2 := make(chan struct{})
	recovered := make(chan struct{}, 1)
	go func() {
		defer close(done2)
		_ = RunWorker(ctx2, publisher, func(context.Context, Task) error { recovered <- struct{}{}; return nil })
	}()
	select {
	case <-recovered:
	case <-time.After(10 * time.Second):
		t.Fatal("unacknowledged task was lost")
	}
	stop2()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("second consumer did not exit")
	}
	t.Log("persistent confirms, mandatory returns, 3 delayed attempts, parking malformed/exhausted tasks, publisher reconnect and unacked redelivery passed")
}

type failingStorage struct{ err error }

func (f failingStorage) StoreEncrypted([]byte, string) error          { return f.err }
func (f failingStorage) StoreThumbnail([]byte, string) error          { return f.err }
func (f failingStorage) GetDecrypted(string) ([]byte, error)          { return nil, f.err }
func (f failingStorage) GetDecryptedThumbnail(string) ([]byte, error) { return nil, f.err }
func (f failingStorage) Delete(string) error                          { return f.err }

func TestDeleteFailureReachesRetryBoundary(t *testing.T) {
	want := errors.New("storage temporarily unavailable")
	task, _ := NewImageDeleteTask("a.jpg")
	if err := NewTaskHandler(failingStorage{want}).Handle(context.Background(), task); !errors.Is(err, want) {
		t.Fatalf("delete error swallowed: %v", err)
	}
	task.Payload = []byte(`{"filename":""}`)
	if err := NewTaskHandler(failingStorage{nil}).Handle(context.Background(), task); !errors.Is(err, ErrPermanent) {
		t.Fatalf("invalid payload not permanent: %v", err)
	}
}
