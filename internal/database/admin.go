package database

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"net/mail"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/models"
	"strings"
	"time"
)

// EnsureAdmin creates the initial administrator once. Existing users are never
// promoted or overwritten; changing environment values is not password rotation.
func EnsureAdmin(db *gorm.DB, admin config.AdminConfig) error {
	name, email := strings.TrimSpace(admin.Name), strings.ToLower(strings.TrimSpace(admin.Email))
	if name == "" || email == "" || strings.TrimSpace(admin.Password) == "" {
		return errors.New("ADMIN_NAME, ADMIN_EMAIL and ADMIN_PASSWORD are required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return errors.New("ADMIN_EMAIL must be a valid email address")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var matches []models.User
		if err := tx.Unscoped().Where("LOWER(email) = ?", email).Find(&matches).Error; err != nil {
			return fmt.Errorf("check administrator: %w", err)
		}
		if len(matches) > 1 {
			return errors.New("administrator email has case-insensitive duplicates")
		}
		if len(matches) == 1 {
			existing := matches[0]
			if existing.DeletedAt.Valid || !existing.IsAdmin {
				return errors.New("administrator email belongs to a deleted or non-admin user")
			}
			if existing.Email != email {
				return tx.Unscoped().Model(&existing).Update("email", email).Error
			}
			return nil
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(admin.Password), bcrypt.DefaultCost)
		if err != nil {
			return errors.New("invalid administrator password")
		}
		verified := time.Now().UTC()
		user := models.User{Name: name, Email: email, Password: string(hash), IsAdmin: true, EmailVerifiedAt: &verified}
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("create administrator: %w", err)
		}
		return nil
	})
}
