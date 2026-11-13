package api

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"strconv"
	"time"
)

type ImageListItem struct {
	ID        uint      `json:"id"`
	Filename  string    `json:"filename"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

func (h *ImageHandler) ListImages(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 || page > 1000000 {
		c.JSON(400, gin.H{"error": "Invalid page"})
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if err != nil || limit < 1 || limit > 100 {
		c.JSON(400, gin.H{"error": "Invalid limit"})
		return
	}
	var total int64
	db := database.GetDB().WithContext(c.Request.Context())
	if err := db.Table("users_images").Where("user_id = ? AND deleted_at IS NULL", user.ID).Count(&total).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to list images"})
		return
	}
	type row struct {
		ID        uint
		Filename  string
		SizeBytes int64
		CreatedAt time.Time
	}
	var images []row
	if err := db.Table("users_images i").Select("i.id, i.image AS filename, COALESCE(o.size_bytes, 0) AS size_bytes, i.created_at").Joins("LEFT JOIN upload_operations o ON o.filename = i.image AND o.user_id = i.user_id").Where("i.user_id = ? AND i.deleted_at IS NULL", user.ID).Order("i.created_at DESC, i.id DESC").Limit(limit).Offset((page - 1) * limit).Scan(&images).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to list images"})
		return
	}
	result := make([]ImageListItem, 0, len(images))
	for _, image := range images {
		result = append(result, ImageListItem{image.ID, image.Filename, image.SizeBytes, image.CreatedAt})
	}
	c.JSON(200, gin.H{"images": result, "page": page, "limit": limit, "total": total})
}
