package limits

import (
	"errors"
	"gorm.io/gorm"
	"time"
)

type UserLimit struct {
	UserID      uint      `gorm:"primaryKey" json:"user_id"`
	AccessTo    time.Time `gorm:"not null;index" json:"access_to"`
	MemoryLimit int64     `gorm:"not null" json:"memory_limit"`
	UsedBytes   int64     `gorm:"not null;default:0" json:"used_bytes"`
}

var ErrAccessOrQuota = errors.New("access expired or storage quota exceeded")

// Reserve must be called in the transaction that records the upload operation.
// The conditional update serializes concurrent reservations on the same user row.
func Reserve(tx *gorm.DB, userID uint, size int64, now time.Time) error {
	if size <= 0 {
		return ErrAccessOrQuota
	}
	result := tx.Model(&UserLimit{}).
		Where("user_id = ? AND access_to > ? AND memory_limit >= ? AND used_bytes <= memory_limit - ?", userID, now.UTC(), size, size).
		UpdateColumn("used_bytes", gorm.Expr("used_bytes + ?", size))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAccessOrQuota
	}
	return nil
}

// Release is used once when a previously reserved upload is irrevocably deleted.
// Its caller must make the upload state transition in the same transaction.
func Release(tx *gorm.DB, userID uint, size int64) error {
	if size <= 0 {
		return errors.New("invalid release size")
	}
	result := tx.Model(&UserLimit{}).Where("user_id = ? AND used_bytes >= ?", userID, size).
		UpdateColumn("used_bytes", gorm.Expr("used_bytes - ?", size))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("invalid storage accounting state")
	}
	return nil
}
