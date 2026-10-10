package mailqueue

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/logging"
	mailservice "snap-flow-go/internal/mail"
	"snap-flow-go/internal/models"
	"snap-flow-go/pkg/crypto"
	"time"
)

type ConfirmationMail struct {
	ID          string    `gorm:"primaryKey;size:36"`
	UserID      uint      `gorm:"not null"`
	Email       string    `gorm:"not null"`
	CodeHash    string    `gorm:"not null"`
	Ciphertext  []byte    `gorm:"not null"`
	ExpiresAt   time.Time `gorm:"not null"`
	AvailableAt time.Time `gorm:"not null"`
	Attempts    int       `gorm:"not null;default:0"`
	SentAt      *time.Time
	CancelledAt *time.Time
	CreatedAt   time.Time `gorm:"not null"`
}
type Sender interface {
	SendVerification(context.Context, string, string) error
}

func Enqueue(tx *gorm.DB, userID uint, email, code, secret string) error {
	if secret == "" {
		return errors.New("mail encryption secret is missing")
	}
	var confirmationCode confirmation.Code
	if err := tx.Where("user_id = ?", userID).First(&confirmationCode).Error; err != nil {
		return err
	}
	encrypted, err := crypto.Encrypt([]byte(code), crypto.GenerateKey("confirmation-mail:"+secret))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return tx.Create(&ConfirmationMail{ID: uuid.NewString(), UserID: userID, Email: email, CodeHash: confirmationCode.CodeHash, Ciphertext: encrypted, ExpiresAt: confirmationCode.ExpiresAt, AvailableAt: now, CreatedAt: now}).Error
}
func Dispatch(ctx context.Context, db *gorm.DB, sender Sender, secret string, now time.Time) (bool, error) {
	found := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item ConfirmationMail
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("sent_at IS NULL AND cancelled_at IS NULL AND available_at <= ?", now).Order("created_at").First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var user models.User
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND email_verified_at IS NULL", item.UserID).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Model(&item).Updates(map[string]any{"cancelled_at": now, "ciphertext": []byte{}}).Error
		}
		if err != nil {
			return err
		}
		var verification confirmation.Code
		err = tx.Where("user_id = ?", item.UserID).First(&verification).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || !now.Before(item.ExpiresAt) || (err == nil && verification.CodeHash != item.CodeHash) {
			return tx.Model(&item).Updates(map[string]any{"cancelled_at": now, "ciphertext": []byte{}}).Error
		}
		if err != nil {
			return err
		}
		code, err := crypto.Decrypt(item.Ciphertext, crypto.GenerateKey("confirmation-mail:"+secret))
		stage, kind, smtpCode := "decrypt", "decryption_failed", 0
		if err == nil {
			err = sender.SendVerification(ctx, item.Email, string(code))
			stage, kind = "send", "unknown"
			var diagnostic *mailservice.DeliveryError
			if errors.As(err, &diagnostic) {
				stage, kind, smtpCode = diagnostic.Stage, diagnostic.Kind, diagnostic.SMTPCode
			}
		}
		if err != nil {
			logging.Event(ctx, "WARNING", "confirmation_delivery_retry", "error_code", "delivery_failed", "stage", stage, "reason", kind, "smtp_code", smtpCode, "attempt", item.Attempts+1)
			return tx.Model(&item).Updates(map[string]any{"attempts": item.Attempts + 1, "available_at": now.Add(time.Minute)}).Error
		}
		return tx.Model(&item).Updates(map[string]any{"sent_at": now, "ciphertext": []byte{}}).Error
	})
	return found, err
}
func Run(ctx context.Context, db *gorm.DB, sender Sender, secret string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		for i := 0; i < 50; i++ {
			found, err := Dispatch(ctx, db, sender, secret, time.Now().UTC())
			if err != nil {
				logging.Event(ctx, "ERROR", "confirmation_dispatch_failed", "error_code", "database_operation_failed")
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
