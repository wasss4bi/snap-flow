package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/image"
	"snap-flow-go/pkg/queue"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ImageHandler struct {
	storage     storage.ImageStorage
	queueClient queue.Publisher
}

func NewImageHandler(storage storage.ImageStorage, queueClient queue.Publisher) *ImageHandler {
	return &ImageHandler{
		storage:     storage,
		queueClient: queueClient,
	}
}

// UploadImages обрабатывает загрузку изображений
func (h *ImageHandler) UploadImages(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "Invalid API token",
		})
		return
	}

	// Получаем загруженные файлы
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024*1024)
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "Failed to parse multipart form",
		})
		return
	}

	defer form.RemoveAll()
	files := form.File["images"]
	if len(files) > 32 {
		c.JSON(400, gin.H{"success": false, "message": "Too many images"})
		return
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "No images provided",
		})
		return
	}

	var uploadedFiles []string
	var uploads []models.UploadItem
	queueFailures := 0
	quotaFailures := 0

	for _, fileHeader := range files {
		// Открываем файл
		file, err := fileHeader.Open()
		if err != nil {
			uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, Status: "rejected", Reason: "Unable to read file"})
			continue
		}

		// Читаем данные изображения
		imageData, err := image.ReadImageFromReader(io.LimitReader(file, image.MaxImageSize+1))
		file.Close()
		if err != nil {
			uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, Status: "rejected", Reason: "Unable to read file"})
			continue
		}

		// Валидируем изображение
		if err := image.ValidateImage(imageData, fileHeader.Filename); err != nil {
			uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, Status: "rejected", Reason: "Invalid or unsupported image"})
			continue
		}

		// Генерируем уникальное имя файла
		ext := strings.ToLower(fileHeader.Filename[strings.LastIndex(fileHeader.Filename, "."):])
		filename := uuid.New().String() + ext

		if err := h.accept(c.Request.Context(), imageData, filename, user.ID); err != nil {
			if errors.Is(err, limits.ErrAccessOrQuota) {
				quotaFailures++
				uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, Status: "rejected", Reason: "Access expired or storage quota exceeded"})
				continue
			}
			queueFailures++
			uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, Status: "rejected", Reason: "Unable to accept file"})
			continue
		}

		uploadedFiles = append(uploadedFiles, filename)
		uploads = append(uploads, models.UploadItem{OriginalName: fileHeader.Filename, ID: filename, Status: "processing"})
	}

	if len(uploadedFiles) == 0 {
		status, message := http.StatusBadRequest, "No valid images accepted"
		if quotaFailures > 0 {
			status, message = http.StatusForbidden, "Access expired or storage quota exceeded"
		}
		if queueFailures > 0 {
			status, message = http.StatusServiceUnavailable, "Failed to persist images"
		}
		c.JSON(status, models.ImageUploadResponse{Success: false, Message: message, Uploads: uploads})
		return
	}
	c.JSON(http.StatusAccepted, models.ImageUploadResponse{
		Success: true,
		Files:   uploadedFiles,
		Uploads: uploads,
		Message: fmt.Sprintf("%d files are uploading", len(uploadedFiles)),
	})
}

// GetImageCount возвращает количество изображений пользователя
func (h *ImageHandler) GetImageCount(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.ImageCountResponse{
			Success: false,
			Error:   "Invalid API token",
		})
		return
	}

	var count int64
	if err := database.GetDB().WithContext(c.Request.Context()).Model(&models.UserImage{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ImageCountResponse{
			Success: false,
			Error:   "Failed to count images",
		})
		return
	}

	c.JSON(http.StatusOK, models.ImageCountResponse{
		Success:     true,
		ImagesCount: int(count),
	})
}

// DeleteImage удаляет конкретное изображение
func (h *ImageHandler) DeleteImage(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.DeleteResponse{
			Success: false,
			Message: "Invalid API token",
		})
		return
	}

	imageIDStr := c.Param("id")
	imageID, err := strconv.ParseUint(imageIDStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.DeleteResponse{
			Success: false,
			Message: "Invalid image ID",
		})
		return
	}

	// Ищем изображение
	var userImage models.UserImage
	if err := database.GetDB().WithContext(c.Request.Context()).Where("id = ? AND user_id = ?", imageID, user.ID).First(&userImage).Error; err != nil {
		c.JSON(http.StatusNotFound, models.DeleteResponse{
			Success: false,
			Message: "Image not found",
		})
		return
	}

	if err := deleteTransaction(c.Request.Context(), []models.UserImage{userImage}); err != nil {
		c.JSON(500, models.DeleteResponse{Success: false, Message: "Failed to persist deletion"})
		return
	}

	c.JSON(http.StatusOK, models.DeleteResponse{
		Success: true,
		Message: "Image deletion scheduled",
	})
}

// DeleteAllImages удаляет все изображения пользователя
func (h *ImageHandler) DeleteAllImages(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.DeleteResponse{
			Success: false,
			Message: "Invalid API token",
		})
		return
	}

	count, err := deleteAllTransaction(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(500, models.DeleteResponse{Success: false, Message: "Failed to persist deletion"})
		return
	}
	if count == 0 {
		c.JSON(http.StatusNotFound, models.DeleteResponse{
			Success: false,
			Message: "No images found",
		})
		return
	}

	c.JSON(http.StatusOK, models.DeleteResponse{
		Success: true,
		Message: "Image deletions scheduled",
	})
}

// ViewImage возвращает изображение для просмотра
func (h *ImageHandler) ViewImage(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	filename := c.Param("filename")

	// Проверяем, принадлежит ли изображение пользователю
	var userImage models.UserImage
	if err := database.GetDB().WithContext(c.Request.Context()).Where("image = ? AND user_id = ?", filename, user.ID).First(&userImage).Error; err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Получаем расшифрованное изображение из хранилища
	imageData, err := h.storage.GetDecrypted(filename)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Определяем MIME-тип
	mimeType := image.GetMimeType(filename)

	c.Data(http.StatusOK, mimeType, imageData)
}

// ViewThumbnail возвращает миниатюру изображения
func (h *ImageHandler) ViewThumbnail(c *gin.Context) {
	user, err := auth.GetCurrentUser(c)
	if err != nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	filename := c.Param("filename")

	// Проверяем, принадлежит ли изображение пользователю
	var userImage models.UserImage
	if err := database.GetDB().WithContext(c.Request.Context()).Where("image = ? AND user_id = ?", filename, user.ID).First(&userImage).Error; err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Получаем расшифрованную миниатюру из хранилища
	thumbnailData, err := h.storage.GetDecryptedThumbnail(filename)
	if err != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Миниатюры всегда в формате JPEG
	c.Data(http.StatusOK, "image/jpeg", thumbnailData)
}
