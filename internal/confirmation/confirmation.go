package confirmation

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Code struct {
	UserID    uint      `gorm:"primaryKey"`
	CodeHash  string    `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null"`
	Attempts  int       `gorm:"not null"`
	CreatedAt time.Time
}

func (Code) TableName() string { return "email_confirmation_codes" }

var ErrInvalid = errors.New("invalid or expired confirmation code")
var ErrCooldown = errors.New("confirmation email requested too soon")

// Issue requires a transaction holding the user's row lock. This ensures that
// confirmation, replacement and concurrent requests cannot race each other.
func Issue(tx *gorm.DB, userID uint, now time.Time) (string, error) {
	now = now.UTC()
	var old Code
	err := tx.Where("user_id = ?", userID).First(&old).Error
	if err == nil && now.Before(old.CreatedAt.Add(time.Minute)) {
		return "", ErrCooldown
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	digest, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	row := Code{UserID: userID, CodeHash: string(digest), ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now}
	err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"code_hash", "expires_at", "attempts", "created_at"})}).Create(&row).Error
	return code, err
}

// Check commits failed attempts; callers must not roll back the transaction on
// a false result. The user row must be locked before calling this function.
func Check(tx *gorm.DB, userID uint, code string, now time.Time) (bool, error) {
	if len(code) != 6 {
		return false, nil
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false, nil
		}
	}
	var row Code
	err := tx.Where("user_id = ?", userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.Attempts >= 5 || !now.UTC().Before(row.ExpiresAt) {
		return false, nil
	}
	if err := tx.Model(&Code{}).Where("user_id = ?", userID).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error; err != nil {
		return false, err
	}
	return bcrypt.CompareHashAndPassword([]byte(row.CodeHash), []byte(code)) == nil, nil
}
