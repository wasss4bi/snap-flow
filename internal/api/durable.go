package api

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/logging"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/queue"
	"time"
)

func (h *ImageHandler) accept(ctx context.Context, data []byte, name string, userID uint) error {
	stage, ok := h.storage.(storage.StagingStorage)
	if !ok {
		return errors.New("staging unavailable")
	}
	db := database.GetDB().WithContext(ctx)
	op := models.UploadOperation{Filename: name, UserID: userID, State: "staging", SizeBytes: int64(len(data)), RequestID: logging.RequestID(ctx)}
	if err := db.Create(&op).Error; err != nil {
		return err
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, "filename = ?", name).Error; err != nil {
			return err
		}
		if op.State != "staging" {
			return errors.New("upload cancelled")
		}
		if err := limits.Reserve(tx, userID, int64(len(data)), time.Now()); err != nil {
			return err
		}
		if err := storage.StageContext(ctx, stage, data, name); err != nil {
			return err
		}
		if err := tx.Model(&op).Update("state", "accepted").Error; err != nil {
			return err
		}
		task, err := queue.NewImageProcessTask(name, name, userID)
		if err != nil {
			return err
		}
		return queue.Record(tx, task, logging.RequestID(ctx))
	})
	if err != nil {
		// Commit may have succeeded even if its acknowledgement was lost.
		// Only a reservation that is still staging can be abandoned safely.
		_ = db.Model(&op).Where("state = ?", "staging").Update("state", "abandoned").Error
	}
	return err
}

func deleteAllTransaction(ctx context.Context, userID uint) (int, error) {
	count := 0
	err := database.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, userID).Error; err != nil {
			return err
		}
		var ops []models.UploadOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND state IN ?", userID, []string{"staging", "accepted", "complete"}).Order("filename").Find(&ops).Error; err != nil {
			return err
		}
		for _, op := range ops {
			updates := map[string]any{"state": "deleting"}
			if op.State == "staging" {
				updates["size_bytes"] = 0
			}
			if err := tx.Model(&op).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&maintenance.Deletion{Filename: op.Filename, AvailableAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
			count++
		}
		var images []models.UserImage
		if err := tx.Where("user_id = ?", userID).Find(&images).Error; err != nil {
			return err
		}
		for _, item := range images {
			initial := models.UploadOperation{Filename: item.Image, UserID: userID, State: "deleting"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&maintenance.Deletion{Filename: item.Image, AvailableAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		count += len(images)
		return tx.Where("user_id = ?", userID).Delete(&models.UserImage{}).Error
	})
	return count, err
}
func deleteTransaction(ctx context.Context, images []models.UserImage) error {
	return database.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range images {
			initial := models.UploadOperation{Filename: item.Image, UserID: item.UserID, State: "complete"}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
				return err
			}
			var op models.UploadOperation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("filename = ? AND user_id = ?", item.Image, item.UserID).First(&op).Error; err != nil {
				return err
			}
			if op.State == "deleted" || op.State == "deleting" {
				continue
			}
			if err := tx.Model(&op).Update("state", "deleting").Error; err != nil {
				return err
			}
			if err := tx.Where("id = ? AND user_id = ?", item.ID, item.UserID).Delete(&models.UserImage{}).Error; err != nil {
				return err
			}
			deletion := maintenance.Deletion{Filename: item.Image, AvailableAt: time.Now().UTC()}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&deletion).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
