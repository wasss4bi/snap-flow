package invite

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Code records an invitation without storing its bearer secret in plaintext.
type Code struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	CodeHash    string     `gorm:"uniqueIndex;not null;size:64" json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `gorm:"not null;index" json:"expires_at"`
	AccessTo    time.Time  `gorm:"not null" json:"access_to"`
	MemoryLimit int64      `gorm:"not null" json:"memory_limit"`
	UsedAt      *time.Time `gorm:"index" json:"used_at"`
}

func (Code) TableName() string { return "invite_codes" }

var ErrInvalid = errors.New("invalid or expired invitation")

func hash(code string) string {
	value := sha256.Sum256([]byte(code))
	return hex.EncodeToString(value[:])
}

// Create returns the secret exactly once and persists only its hash.
func Create(db *gorm.DB, accessTo time.Time, limit int64, now time.Time) (Code, string, error) {
	now = now.UTC()
	accessTo = accessTo.UTC()
	if limit <= 0 || !accessTo.After(now) {
		return Code{}, "", errors.New("access_to must be in the future and memory_limit must be positive")
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return Code{}, "", err
	}
	secret := hex.EncodeToString(value)
	invitation := Code{CodeHash: hash(secret), CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), AccessTo: accessTo, MemoryLimit: limit}
	if err := db.Create(&invitation).Error; err != nil {
		return Code{}, "", err
	}
	return invitation, secret, nil
}

// Consume must be called in the same transaction as user creation. Its
// conditional update prevents concurrent registrations from sharing a code.
func Consume(tx *gorm.DB, secret string, now time.Time) (Code, error) {
	if len(secret) != 64 {
		return Code{}, ErrInvalid
	}
	now = now.UTC()
	var code Code
	if err := tx.Where("code_hash = ?", hash(secret)).First(&code).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Code{}, ErrInvalid
		}
		return Code{}, err
	}
	result := tx.Model(&Code{}).Where("id = ? AND used_at IS NULL AND expires_at > ? AND access_to > ?", code.ID, now, now).Update("used_at", now)
	if result.Error != nil {
		return Code{}, result.Error
	}
	if result.RowsAffected != 1 {
		return Code{}, ErrInvalid
	}
	code.UsedAt = &now
	return code, nil
}

func DeleteExpiredUnused(db *gorm.DB, now time.Time) error {
	return db.Where("used_at IS NULL AND expires_at <= ?", now.UTC()).Delete(&Code{}).Error
}
