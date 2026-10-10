package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/mailqueue"
	"snap-flow-go/internal/models"
)

func (h *AuthHandler) Register(c *gin.Context) {
	var req models.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil || !validRegistration(req.Name, req.Email, req.Password) {
		c.JSON(400, gin.H{"success": false, "message": "Invalid request data"})
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	db := database.GetDB().WithContext(c.Request.Context())
	var existing models.User
	if err := db.Unscoped().Where("email = ?", req.Email).First(&existing).Error; err == nil {
		c.JSON(409, gin.H{"success": false, "message": "User with this email already exists"})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(500, gin.H{"success": false, "message": "Registration failed"})
		return
	}
	password, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Invalid password"})
		return
	}
	var user models.User
	var code string
	err = db.Transaction(func(tx *gorm.DB) error {
		invitation, err := invite.Consume(tx, req.InviteCode, time.Now())
		if err != nil {
			return err
		}
		user = models.User{Name: req.Name, Email: req.Email, Password: string(password)}
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := tx.Create(&limits.UserLimit{UserID: user.ID, AccessTo: invitation.AccessTo, MemoryLimit: invitation.MemoryLimit}).Error; err != nil {
			return err
		}
		code, err = confirmation.Issue(tx, user.ID, time.Now())
		if err != nil {
			return err
		}
		return mailqueue.Enqueue(tx, user.ID, user.Email, code, h.mailSecret)
	})
	if errors.Is(err, invite.ErrInvalid) {
		c.JSON(400, gin.H{"success": false, "message": "Invalid or expired invitation"})
		return
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && (pgErr.ConstraintName == "users_email_key" || pgErr.ConstraintName == "uni_users_email") {
		c.JSON(409, gin.H{"success": false, "message": "User with this email already exists"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Registration failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "message": "Check your email for the confirmation code"})
}

func validRegistration(name, email, password string) bool {
	if utf8.RuneCountInString(name) < 3 || utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return false
	}
	for _, s := range []string{name, email, password} {
		for _, r := range s {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return false
			}
		}
	}
	var upper, lower, digit, special bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			special = true
		}
	}
	return upper && lower && digit && special
}
func (h *AuthHandler) VerifyEmail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req struct {
		Email string `json:"email" binding:"required,email"`
		Code  string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Invalid request data"})
		return
	}
	var ok bool
	err := database.GetDB().WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ? AND email_verified_at IS NULL", strings.ToLower(strings.TrimSpace(req.Email))).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var err error
		ok, err = confirmation.Check(tx, user.ID, req.Code, time.Now())
		if err != nil || !ok {
			return err
		}
		if err = tx.Model(&user).Update("email_verified_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", user.ID).Delete(&confirmation.Code{}).Error
	})
	if err != nil {
		c.JSON(500, gin.H{"success": false, "message": "Verification failed"})
		return
	}
	if !ok {
		c.JSON(400, gin.H{"success": false, "message": "Invalid or expired confirmation code"})
		return
	}
	c.JSON(200, gin.H{"success": true, "message": "Email confirmed"})
}
func (h *AuthHandler) ResendVerification(c *gin.Context) {
	var req struct {
		Email string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Invalid request data"})
		return
	}
	var user models.User
	var code string
	err := database.GetDB().WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ? AND email_verified_at IS NULL", strings.ToLower(strings.TrimSpace(req.Email))).First(&user).Error; err != nil {
			return err
		}
		var err error
		code, err = confirmation.Issue(tx, user.ID, time.Now())
		if err != nil {
			return err
		}
		return mailqueue.Enqueue(tx, user.ID, user.Email, code, h.mailSecret)
	})
	_ = err
	c.JSON(200, gin.H{"success": true, "message": "If the account requires confirmation, an email will be sent"})
}
