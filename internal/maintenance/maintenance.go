package maintenance

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/logging"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

type Notice struct {
	ID          uint      `gorm:"primaryKey"`
	UserID      uint      `gorm:"uniqueIndex:notice_period;not null"`
	AccessTo    time.Time `gorm:"uniqueIndex:notice_period;not null"`
	Email       string    `gorm:"not null"`
	Attempts    int
	AvailableAt time.Time  `gorm:"index"`
	SentAt      *time.Time `gorm:"index"`
	CreatedAt   time.Time
}
type Deletion struct {
	Filename    string `gorm:"primaryKey;size:128"`
	Attempts    int
	AvailableAt time.Time  `gorm:"index"`
	CompletedAt *time.Time `gorm:"index"`
}
type Mailer interface {
	SendExpiry(context.Context, string, time.Time) error
}

// Schedule records one notification per expired access period and fences old
// uploads before recording physical deletions. Row locks serialize extensions.
func Schedule(ctx context.Context, db *gorm.DB, now time.Time) error {
	db = db.WithContext(ctx)
	now = now.UTC()
	if err := db.Transaction(func(tx *gorm.DB) error {
		var expired []models.UploadOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state IN ? AND COALESCE(failure_code, '') <> ? AND size_bytes > 0 AND first_failed_at <= ?", []string{"failed", "accepted"}, "invalid_image", now.Add(-7*24*time.Hour)).Limit(100).Find(&expired).Error; err != nil {
			return err
		}
		for _, op := range expired {
			if op.State == "accepted" {
				if err := tx.Model(&op).Updates(map[string]any{"state": "failed", "failure_code": "processing_failed", "failed_at": now}).Error; err != nil {
					return err
				}
			}
			item := Deletion{Filename: op.Filename, AvailableAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := db.Where("expires_at <= ?", now).Delete(&models.RevokedToken{}).Error; err != nil {
		return err
	}
	if err := db.Where("state = ? AND failed_at < ? AND EXISTS (SELECT 1 FROM deletions d WHERE d.filename = upload_operations.filename AND d.completed_at IS NOT NULL)", "failed", now.Add(-30*24*time.Hour)).Delete(&models.UploadOperation{}).Error; err != nil {
		return err
	}
	if err := invite.DeleteExpiredUnused(db, now); err != nil {
		return err
	}
	var ids []uint
	if err := db.Model(&limits.UserLimit{}).Where("access_to <= ?", now).Pluck("user_id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		err := db.Transaction(func(tx *gorm.DB) error {
			var user models.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			} else if err != nil {
				return err
			}
			var limit limits.UserLimit
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ?", id).First(&limit).Error; err != nil {
				return err
			}
			if limit.AccessTo.After(now) {
				return nil
			}
			notice := Notice{UserID: id, AccessTo: limit.AccessTo, Email: user.Email, AvailableAt: now}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&notice).Error; err != nil {
				return err
			}
			if now.Before(limit.AccessTo.Add(15 * 24 * time.Hour)) {
				return nil
			}
			var ops []models.UploadOperation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND state IN ?", id, []string{"accepted", "complete"}).Find(&ops).Error; err != nil {
				return err
			}
			for _, op := range ops {
				if err := tx.Model(&op).Update("state", "deleting").Error; err != nil {
					return err
				}
				item := Deletion{Filename: op.Filename, AvailableAt: now}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
					return err
				}
			}
			var legacy []models.UserImage
			if err := tx.Unscoped().Where("user_id = ? AND NOT EXISTS (SELECT 1 FROM upload_operations o WHERE o.filename = users_images.image)", id).Find(&legacy).Error; err != nil {
				return err
			}
			for _, image := range legacy {
				op := models.UploadOperation{Filename: image.Image, UserID: id, State: "deleting"}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&op).Error; err != nil {
					return err
				}
				item := Deletion{Filename: image.Image, AvailableAt: now}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
					return err
				}
			}
			return tx.Where("user_id = ?", id).Delete(&models.UserImage{}).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func DispatchNotice(ctx context.Context, db *gorm.DB, mailer Mailer, now time.Time) (bool, error) {
	found := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item Notice
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("sent_at IS NULL AND available_at <= ?", now).Order("id").First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		if err := mailer.SendExpiry(ctx, item.Email, item.AccessTo.Add(15*24*time.Hour)); err != nil {
			logging.Event(ctx, "WARNING", "expiry_notification_retry", "error_code", "delivery_failed")
			return tx.Model(&item).Updates(map[string]any{"attempts": item.Attempts + 1, "available_at": now.Add(time.Minute)}).Error
		}
		return tx.Model(&item).Update("sent_at", now).Error
	})
	return found, err
}
func DispatchDeletion(ctx context.Context, db *gorm.DB, store storage.ImageStorage, now time.Time) (bool, error) {
	found := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item Deletion
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("completed_at IS NULL AND available_at <= ?", now).First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var op models.UploadOperation
		opErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("filename = ?", item.Filename).First(&op).Error
		if opErr != nil && !errors.Is(opErr, gorm.ErrRecordNotFound) {
			return opErr
		}
		err = store.Delete(item.Filename)
		if err == nil {
			if stage, ok := store.(storage.StagingStorage); ok {
				err = stage.DeleteStaged(item.Filename)
			}
		}
		if err != nil {
			logging.Event(ctx, "WARNING", "image_deletion_retry", "error_code", "storage_delete_failed")
			return tx.Model(&item).Updates(map[string]any{"attempts": item.Attempts + 1, "available_at": now.Add(time.Minute)}).Error
		}
		if opErr == nil && (op.State == "deleting" || op.State == "failed") {
			if op.SizeBytes > 0 {
				if err := limits.Release(tx, op.UserID, op.SizeBytes); err != nil {
					return err
				}
			}
			state := op.State
			if state == "deleting" {
				state = "deleted"
			}
			if err := tx.Model(&op).Updates(map[string]any{"state": state, "size_bytes": 0}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&item).Update("completed_at", now).Error
	})
	return found, err
}
func Run(ctx context.Context, db *gorm.DB, store storage.ImageStorage, mailer Mailer) {
	go runNotices(ctx, db, mailer)
	go runDeletions(ctx, db, store)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		if err := Schedule(ctx, db, now); err != nil {
			logging.Event(ctx, "ERROR", "maintenance_schedule_failed", "error_code", "database_operation_failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runNotices(ctx context.Context, db *gorm.DB, mailer Mailer) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		for i := 0; i < 100; i++ {
			found, err := DispatchNotice(ctx, db, mailer, now)
			if err != nil {
				logging.Event(ctx, "ERROR", "notice_dispatch_failed", "error_code", "database_operation_failed")
			}
			if err != nil || !found {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runDeletions(ctx context.Context, db *gorm.DB, store storage.ImageStorage) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		for i := 0; i < 100; i++ {
			found, err := DispatchDeletion(ctx, db, store, now)
			if err != nil {
				logging.Event(ctx, "ERROR", "deletion_dispatch_failed", "error_code", "database_operation_failed")
			}
			if err != nil || !found {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
