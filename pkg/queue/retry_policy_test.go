package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"snap-flow-go/internal/config"

	amqp "github.com/rabbitmq/amqp091-go"
)

type recordedAck struct{ acked int }

func (a *recordedAck) Ack(uint64, bool) error      { a.acked++; return nil }
func (*recordedAck) Nack(uint64, bool, bool) error { return errors.New("unexpected nack") }
func (*recordedAck) Reject(uint64, bool) error     { return errors.New("unexpected reject") }

func TestRetryScheduleReachesOneHour(t *testing.T) {
	got := config.RabbitRetryDelaysMS()
	want := []int{5000, 30000, 300000, 900000, 3600000}
	if len(got) != len(want) {
		t.Fatalf("schedule: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("schedule: %v", got)
		}
	}
	p, err := NewRabbitPublisher(config.Load().RabbitMQ)
	if err != nil || p.MaxAttempts() != len(want)+1 {
		t.Fatalf("publisher=%v err=%v", p, err)
	}
}

func TestInvalidImageIsAcknowledgedWithoutParking(t *testing.T) {
	task, err := NewImageProcessTask("bad.png", "bad.png", 1)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	ack := &recordedAck{}
	p := &RabbitPublisher{cfg: config.Load().RabbitMQ}
	delivery := amqp.Delivery{Body: body, Acknowledger: ack, DeliveryTag: 1}
	err = handleDelivery(context.Background(), p, func(context.Context, Task) error {
		return errors.Join(ErrPermanent, ErrInvalidImage)
	}, delivery)
	if err != nil || ack.acked != 1 {
		t.Fatalf("err=%v acked=%d", err, ack.acked)
	}
}
