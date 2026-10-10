package queue

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/logging"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"time"
)

// Record must use the same transaction as its business mutation.
func Record(tx *gorm.DB, task Task, requestID string) error {
	if requestID != "" {
		task.RequestID = requestID
	}
	body, err := json.Marshal(task)
	if err != nil {
		return err
	}
	return tx.Create(&models.OutboxEvent{ID: task.ID, RequestID: task.RequestID, Body: string(body), AvailableAt: time.Now()}).Error
}

func parkOutbox(ctx context.Context, tx *gorm.DB, publisher Publisher, event models.OutboxEvent, task Task, attempts int) error {
	if task.Type == TypeImageProcess {
		var payload ImageProcessPayload
		if json.Unmarshal(task.Payload, &payload) == nil && payload.UserID != 0 && payload.Filename != "" {
			if err := markUploadFailure(tx, task, "queue_unavailable"); err != nil {
				return err
			}
		}
	}
	parking, ok := publisher.(interface {
		PublishFailure(context.Context, []byte, string) error
	})
	if ok && parking.PublishFailure(ctx, []byte(event.Body), "outbox_exhausted") == nil {
		return tx.Model(&event).Updates(map[string]any{"attempts": attempts, "failed": true}).Error
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	logging.Event(logging.WithRequestID(ctx, event.RequestID), "WARNING", "outbox_parking_pending", "event_id", event.ID, "error_code", "parking_unavailable")
	return tx.Model(&event).Updates(map[string]any{"attempts": attempts, "available_at": time.Now().Add(time.Minute)}).Error
}

// A PostgreSQL row lock is retained until confirmation and the status update
// commit. A process crash releases it; an uncertain commit can cause a duplicate.
func DispatchOnce(ctx context.Context, db *gorm.DB, publisher Publisher) (bool, error) {
	found := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event models.OutboxEvent
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("published_at IS NULL AND failed = ? AND available_at <= ?", false, time.Now()).Order("created_at, id").First(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		task, err := decodeTask([]byte(event.Body))
		delays := config.RabbitRetryDelaysMS()
		if event.Attempts > len(delays) {
			return parkOutbox(ctx, tx, publisher, event, task, event.Attempts)
		}
		if err == nil {
			err = publisher.Publish(logging.WithRequestID(ctx, event.RequestID), task)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			attempts := event.Attempts + 1
			if attempts > len(delays) {
				// RabbitMQ may itself be unavailable. Keep the event durable and
				// retry until the terminal message can actually be parked.
				return parkOutbox(ctx, tx, publisher, event, task, attempts)
			}
			delay := time.Duration(delays[attempts-1]) * time.Millisecond
			logging.Event(logging.WithRequestID(ctx, event.RequestID), "WARNING", "outbox_publish_failed", "event_id", event.ID, "attempt", attempts, "error_code", "publication_unconfirmed")
			return tx.Model(&event).Updates(map[string]any{"attempts": attempts, "available_at": time.Now().Add(delay)}).Error
		}
		now := time.Now()
		if err := tx.Model(&event).Update("published_at", now).Error; err != nil {
			return err
		}
		logging.Event(logging.WithRequestID(ctx, event.RequestID), "INFO", "outbox_published", "event_id", event.ID)
		return nil
	})
	return found, err
}

func CollectStaging(ctx context.Context, db *gorm.DB, store storage.StagingStorage) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var operations []models.UploadOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state IN ? AND updated_at < ?", []string{"staging", "abandoned", "complete", "deleted"}, time.Now().Add(-time.Hour)).Limit(20).Find(&operations).Error; err != nil {
			return err
		}
		for _, op := range operations {
			if err := store.DeleteStaged(op.Filename); err != nil {
				return err
			}
			state := op.State
			if state == "staging" {
				state = "abandoned"
			}
			if err := tx.Model(&op).Updates(map[string]any{"state": state, "updated_at": time.Now()}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func RunOutbox(ctx context.Context, publisher Publisher, store storage.StagingStorage) {
	cleanup := time.NewTicker(time.Minute)
	defer cleanup.Stop()
	for ctx.Err() == nil {
		found, err := DispatchOnce(ctx, database.GetDB(), publisher)
		if err != nil {
			logging.Event(ctx, "ERROR", "outbox_database_failed", "error_code", "transaction_failed")
		}
		select {
		case <-cleanup.C:
			if err := CollectStaging(ctx, database.GetDB(), store); err != nil {
				logging.Event(ctx, "WARNING", "staging_cleanup_failed", "error_code", "cleanup_failed")
			}
			if err := database.GetDB().WithContext(ctx).Where("expires_at < ?", time.Now().UTC()).Delete(&models.UploadIdempotency{}).Error; err != nil {
				logging.Event(ctx, "WARNING", "idempotency_cleanup_failed", "error_code", "cleanup_failed")
			}
		default:
		}
		if !found || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}
