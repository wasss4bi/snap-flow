package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
)

type AdminHandler struct{}

func (h AdminHandler) CreateInvite(c *gin.Context) {
	var req struct {
		AccessTo    time.Time `json:"access_to" binding:"required"`
		MemoryLimit int64     `json:"memory_limit" binding:"required,gt=0"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !req.AccessTo.After(time.Now()) {
		c.JSON(400, gin.H{"error": "access_to must be a future timestamp and memory_limit must be positive"})
		return
	}
	row, code, err := invite.Create(database.GetDB().WithContext(c.Request.Context()), req.AccessTo, req.MemoryLimit, time.Now())
	if err != nil {
		c.JSON(500, gin.H{"error": "Could not create invitation"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{"invite_code": code, "invitation": row})
}
func (h AdminHandler) ListUsers(c *gin.Context) {
	type row struct {
		ID              uint       `json:"id"`
		Name            string     `json:"name"`
		Email           string     `json:"email"`
		EmailVerifiedAt *time.Time `json:"email_verified_at"`
		AccessTo        time.Time  `json:"access_to"`
		MemoryLimit     int64      `json:"memory_limit"`
		UsedBytes       int64      `json:"used_bytes"`
	}
	var users []row
	err := database.GetDB().WithContext(c.Request.Context()).Table("users").Select("users.id, users.name, users.email, users.email_verified_at, user_limits.access_to, user_limits.memory_limit, user_limits.used_bytes").Joins("JOIN user_limits ON user_limits.user_id = users.id").Where("users.deleted_at IS NULL").Scan(&users).Error
	if err != nil {
		c.JSON(500, gin.H{"error": "Could not load users"})
		return
	}
	c.JSON(200, gin.H{"users": users})
}
func (h AdminHandler) UpdateLimits(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(400, gin.H{"error": "Invalid user ID"})
		return
	}
	var req struct {
		AccessTo    time.Time `json:"access_to" binding:"required"`
		MemoryLimit int64     `json:"memory_limit" binding:"required,gt=0"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid limits"})
		return
	}
	err = database.GetDB().WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, id).Error; err != nil {
			return err
		}
		if user.IsAdmin {
			return gorm.ErrRecordNotFound
		}
		result := tx.Model(&limits.UserLimit{}).Where("user_id = ?", id).Updates(map[string]any{"access_to": req.AccessTo.UTC(), "memory_limit": req.MemoryLimit})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "User not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "Could not update limits"})
		return
	}
	c.JSON(200, gin.H{"success": true})
}
