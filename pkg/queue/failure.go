package queue

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"time"
)

var ErrFailurePersistence = errors.New("could not persist final upload failure")

func markUploadFailure(tx *gorm.DB, task Task, reason string) error {
	if task.Type != TypeImageProcess {
		return nil
	}
	var payload ImageProcessPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return err
	}
	var op models.UploadOperation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("filename = ? AND user_id = ?", payload.Filename, payload.UserID).First(&op).Error; err != nil {
		return err
	}
	if op.State != "accepted" {
		return nil
	}
	now := time.Now().UTC()
	if err := tx.Model(&op).Updates(map[string]any{"state": "failed", "failure_code": reason, "failed_at": now}).Error; err != nil {
		return err
	}
	item := maintenance.Deletion{Filename: op.Filename, AvailableAt: now}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error
}
func HandleWithFailureTracking(handler Handler, maxAttempts int) Handler {
	return func(ctx context.Context, task Task) error {
		err := callHandler(ctx, handler, task)
		if err != nil && task.Type == TypeImageProcess && (errors.Is(err, ErrPermanent) || task.Attempt+1 >= maxAttempts) && ctx.Err() == nil {
			reason := "processing_failed"
			if errors.Is(err, ErrInvalidImage) {
				reason = "invalid_image"
			}
			if e := database.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error { return markUploadFailure(tx, task, reason) }); e != nil {
				return errors.Join(ErrFailurePersistence, e)
			}
		}
		return err
	}
}
