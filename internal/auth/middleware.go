package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"net/http"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"strings"

	"github.com/gin-gonic/gin"
)

// JWTMiddleware проверяет JWT токен для веб-интерфейса
func JWTMiddleware(jwtManager *JWTManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Получаем токен из cookie или заголовка Authorization
		var tokenString string

		// Сначала проверяем cookie
		if authHeader := c.GetHeader("Authorization"); authHeader != "" {
			// Затем проверяем заголовок Authorization

			// Извлекаем токен из "Bearer <token>"
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
				c.Abort()
				return
			}
			tokenString = parts[1]
		} else if cookie, err := c.Cookie("auth_token"); err == nil {
			tokenString = cookie
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization required"})
			return
		}

		// Валидируем токен
		claims, err := jwtManager.ValidateToken(tokenString)
		if err != nil || claims.ExpiresAt == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}

		var revoked models.RevokedToken
		err = database.GetDB().WithContext(c.Request.Context()).First(&revoked, "hash = ?", TokenHash(tokenString)).Error
		if err == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Authentication temporarily unavailable"})
			return
		}
		c.Set("token_hash", TokenHash(tokenString))
		c.Set("token_expires_at", claims.ExpiresAt.Time)
		var user models.User
		if err := database.GetDB().WithContext(c.Request.Context()).First(&user, claims.UserID).Error; err != nil || user.EmailVerifiedAt == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Account unavailable"})
			return
		}
		c.Set("user_id", user.ID)
		c.Set("user_email", user.Email)
		c.Set("is_admin", user.IsAdmin)

		c.Next()
	}
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// AdminMiddleware проверяет права администратора
func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, exists := c.Get("is_admin")
		if !exists || !isAdmin.(bool) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// GetCurrentUser получает текущего пользователя из контекста
func GetCurrentUser(c *gin.Context) (*models.User, error) {
	userID, exists := c.Get("user_id")
	if !exists {
		return nil, fmt.Errorf("user not found in context")
	}

	var user models.User
	if err := database.GetDB().WithContext(c.Request.Context()).First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("failed to get user from database: %w", err)
	}

	return &user, nil
}
