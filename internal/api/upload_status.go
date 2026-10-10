package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"net/http"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
)

func (h *ImageHandler) UploadStatus(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	var op models.UploadOperation
	err = database.GetDB().WithContext(c.Request.Context()).Where("filename = ? AND user_id = ?", c.Param("filename"), user.ID).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "Upload not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "Unable to load upload status"})
		return
	}
	status := "processing"
	reason := ""
	switch op.State {
	case "complete":
		status = "ready"
	case "failed":
		status = "failed"
		switch op.FailureCode {
		case "invalid_image":
			reason = "Image is invalid or cannot be processed"
		case "queue_unavailable":
			reason = "Upload service unavailable"
		default:
			reason = "Image processing failed"
		}
	case "deleting":
		status = "deleting"
	case "deleted":
		status = "deleted"
	case "abandoned":
		status = "failed"
		reason = "Upload could not be accepted"
	}
	c.JSON(http.StatusOK, gin.H{"id": op.Filename, "status": status, "reason": reason, "created_at": op.CreatedAt, "updated_at": op.UpdatedAt})
}
