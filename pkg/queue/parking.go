package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/logging"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/observability"
)

// recoverParked atomically returns an eligible failed operation to the outbox.
// A duplicate delivery after commit sees accepted and is safe to acknowledge.
func recoverParked(ctx context.Context, db *gorm.DB, task Task, now time.Time) (bool, error) {
	if task.Type != TypeImageProcess {
		return false, nil
	}
	var payload ImageProcessPayload
	if json.Unmarshal(task.Payload, &payload) != nil || payload.UserID == 0 || payload.Filename == "" || payload.StagingKey != payload.Filename {
		return false, nil
	}
	ack := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op models.UploadOperation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("filename = ? AND user_id = ?", payload.Filename, payload.UserID).First(&op).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if op.State != "failed" && op.State != "accepted" {
			ack = op.State == "complete" || op.State == "deleted" || op.State == "deleting"
			return nil
		}
		if op.FailureCode == "invalid_image" {
			ack = true
			return nil
		}
		if op.SizeBytes == 0 {
			// Zero can mean either completed cleanup or a legacy operation with
			// no recorded size. Only discard after confirmed physical deletion.
			var completed int64
			if err := tx.Table("deletions").Where("filename = ? AND completed_at IS NOT NULL", op.Filename).Count(&completed).Error; err != nil { return err }
			ack = completed > 0
			return nil
		}
		var recovered models.ParkingRecovery
		if err := tx.First(&recovered, "task_id = ?", task.ID).Error; err == nil {
			ack = true
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		first := op.FirstFailedAt
		if first == nil {
			first = op.FailedAt
		}
		if first != nil && !now.Before(first.Add(7*24*time.Hour)) {
			return nil
		}
		if op.State == "failed" && (op.FailedAt == nil || now.Before(op.FailedAt.Add(time.Hour))) {
			return nil
		}
		if err := tx.Create(&models.ParkingRecovery{TaskID: task.ID, CreatedAt: now}).Error; err != nil {
			return err
		}
		task.ID = uuid.NewString()
		task.Attempt = 0
		if err := Record(tx, task, task.RequestID); err != nil {
			return err
		}
		if err := tx.Model(&op).Updates(map[string]any{"state": "accepted", "failure_code": "", "failed_at": nil, "first_failed_at": gorm.Expr("COALESCE(first_failed_at, ?)", now)}).Error; err != nil {
			return err
		}
		ack = true
		return nil
	})
	return ack, err
}

func recoverOneParked(ctx context.Context, p *RabbitPublisher) error {
	conn, err := dialBroker(p.cfg, time.Duration(p.cfg.PublishTimeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseDeadline(time.Now().Add(time.Second)) }()
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseDeadline(time.Now()) })
	defer stop()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	if err = declareQueues(ch, p.cfg); err != nil {
		return err
	}
	delivery, found, err := ch.Get(p.cfg.QueuePrefix+".parking", false)
	if err != nil || !found {
		return err
	}
	task, err := decodeTask(delivery.Body)
	if err != nil {
		return keepParked(ctx, p, delivery)
	}
	ack, err := recoverParked(ctx, database.GetDB(), task, time.Now().UTC())
	if err != nil {
		if keepErr := keepParked(ctx, p, delivery); keepErr != nil {
			return errors.Join(err, keepErr)
		}
		return err
	}
	if !ack {
		return keepParked(ctx, p, delivery)
	}
	if err := delivery.Ack(false); err != nil {
		return fmt.Errorf("ack recovered task: %w", err)
	}
	return nil
}

func keepParked(ctx context.Context, p *RabbitPublisher, delivery amqp.Delivery) error {
	headers := amqp.Table{}
	for key, value := range delivery.Headers {
		headers[key] = value
	}
	if err := p.publishRaw(ctx, p.cfg.QueuePrefix+".parking", delivery.Body, headers); err != nil {
		return err
	}
	return delivery.Ack(false)
}

// The interval applies per worker instance. Unrecognised messages remain parked.
func RunParkingRecovery(ctx context.Context, p *RabbitPublisher) {
	ticker := time.NewTicker(time.Duration(p.cfg.ParkingReplayIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var result struct{ Oldest *time.Time }
			err := database.GetDB().WithContext(ctx).Model(&models.UploadOperation{}).Select("min(first_failed_at) AS oldest").Where("state IN ? AND first_failed_at IS NOT NULL AND COALESCE(failure_code, '') <> ? AND size_bytes > 0", []string{"failed", "accepted"}, "invalid_image").Scan(&result).Error
			if err != nil {
				observability.OldestFailedUpload.Set(math.NaN())
			} else if result.Oldest == nil {
				observability.OldestFailedUpload.Set(0)
			} else {
				observability.OldestFailedUpload.Set(math.Max(0, time.Since(*result.Oldest).Seconds()))
			}
			if err := recoverOneParked(ctx, p); err != nil && ctx.Err() == nil {
				logging.Event(ctx, "WARNING", "parking_recovery_failed", "error_code", "recovery_failed")
			}
		}
	}
}
