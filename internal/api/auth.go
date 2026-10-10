package api

import (
	"context"
	"errors"
	"net/http"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	mailservice "snap-flow-go/internal/mail"
	"snap-flow-go/internal/models"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	jwtManager *auth.JWTManager
	mailer     mailservice.Sender
	limiter    LoginLimiter
	mailSecret string
}

type LoginLimiter interface {
	Begin(context.Context, string) (string, int, time.Duration, error)
	Finish(context.Context, string, string, string) (int, error)
}

func (h *AuthHandler) SetMailSecret(secret string) { h.mailSecret = secret }

func (h *AuthHandler) SetLoginLimiter(l LoginLimiter) { h.limiter = l }

func NewAuthHandler(jwtManager *auth.JWTManager, senders ...mailservice.Sender) *AuthHandler {
	var sender mailservice.Sender
	if len(senders) > 0 {
		sender = senders[0]
	}
	return &AuthHandler{
		jwtManager: jwtManager,
		mailer:     sender,
	}
}

// Login обрабатывает аутентификацию пользователя
func (h *AuthHandler) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Invalid request data",
			"error":   err.Error(),
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	ip := c.ClientIP()
	var lease string
	if h.limiter != nil {
		var ttl time.Duration
		var err error
		lease, _, ttl, err = h.limiter.Begin(c.Request.Context(), ip)
		if err != nil {
			c.JSON(503, gin.H{"success": false, "message": "Authentication temporarily unavailable"})
			return
		}
		if lease == "" {
			c.Header("Retry-After", strconv.Itoa(int(ttl.Seconds())+1))
			c.JSON(429, gin.H{"success": false, "message": "Too many failed or concurrent attempts", "attempts_remaining": 0})
			return
		}
		defer func() {
			if lease != "" {
				_, _ = h.limiter.Finish(context.Background(), ip, lease, "cancel")
			}
		}()
	}
	failed := func() {
		remaining := 5
		if h.limiter != nil {
			n, err := h.limiter.Finish(c.Request.Context(), ip, lease, "failure")
			lease = ""
			if err != nil || n < 0 {
				c.JSON(503, gin.H{"success": false, "message": "Authentication temporarily unavailable"})
				return
			}
			remaining -= n
			if remaining < 0 {
				remaining = 0
			}
		}
		c.JSON(401, gin.H{"success": false, "message": "Invalid email or password", "attempts_remaining": remaining})
	}
	// Ищем пользователя по email
	var user models.User
	if err := database.GetDB().WithContext(c.Request.Context()).Where("email = ?", req.Email).First(&user).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(503, gin.H{"success": false, "message": "Authentication temporarily unavailable"})
			return
		}
		failed()
		return
	}

	// Проверяем пароль
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		failed()
		return
	}
	if user.EmailVerifiedAt == nil {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "Email confirmation required"})
		return
	}
	if h.limiter != nil {
		n, err := h.limiter.Finish(c.Request.Context(), ip, lease, "success")
		lease = ""
		if err != nil || n < 0 {
			c.JSON(503, gin.H{"success": false, "message": "Authentication temporarily unavailable"})
			return
		}
	}

	// Генерируем JWT токен
	token, err := h.jwtManager.GenerateToken(user.ID, user.Email, user.IsAdmin)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to generate token",
		})
		return
	}

	// Устанавливаем cookie для веб-интерфейса
	c.SetCookie("auth_token", token, int(24*time.Hour.Seconds()), "/", "", false, true)

	c.JSON(http.StatusOK, models.LoginResponse{
		Success:           true,
		Token:             token,
		Message:           "Login successful",
		AttemptsRemaining: 5,
	})
}

// Logout обрабатывает выход пользователя
func (h *AuthHandler) Logout(c *gin.Context) {
	hash, exists := c.Get("token_hash")
	expires, ok := c.Get("token_expires_at")
	if !exists || !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false})
		return
	}
	if err := database.GetDB().WithContext(c.Request.Context()).Clauses(clause.OnConflict{DoNothing: true}).Create(&models.RevokedToken{Hash: hash.(string), ExpiresAt: expires.(time.Time)}).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Logout unavailable"})
		return
	}
	// Удаляем cookie
	c.SetCookie("auth_token", "", -1, "/", "", false, true)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Logout successful",
	})
}

// GetProfile возвращает профиль текущего пользователя
func (h *AuthHandler) GetProfile(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"message": "Failed to get user profile",
		})
		return
	}

	var currentLimits *limits.UserLimit
	if !user.IsAdmin {
		var value limits.UserLimit
		if err := database.GetDB().WithContext(c.Request.Context()).Where("user_id = ?", user.ID).First(&value).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Failed to get storage limits"})
			return
		}
		currentLimits = &value
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"user": gin.H{
			"id":       user.ID,
			"name":     user.Name,
			"email":    user.Email,
			"is_admin": user.IsAdmin,
		},
		"storage_limits": currentLimits,
	})
}
