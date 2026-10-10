package api

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
)

var errIdempotencyConflict = errors.New("idempotency key conflict")
var errInvalidIdempotencyKey = errors.New("invalid idempotency key")
var errRetryableUpload = errors.New("upload can be retried")
var idempotencyKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)

func uploadFingerprint(files []*multipart.FileHeader) (string, error) {
	h := sha256.New()
	var size [8]byte
	for _, f := range files {
		if f.Size < 0 {
			return "", errors.New("invalid multipart size")
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(f.Filename)))
		_, _ = h.Write(size[:])
		_, _ = io.WriteString(h, f.Filename)
		binary.BigEndian.PutUint64(size[:], uint64(f.Size)) // #nosec G115 -- negative sizes are rejected above.
		_, _ = h.Write(size[:])
		reader, err := f.Open()
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, reader)
		closeErr := reader.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (h *ImageHandler) runIdempotentUpload(c *gin.Context, userID uint, key string, files []*multipart.FileHeader) (int, models.ImageUploadResponse, error) {
	db := database.GetDB().WithContext(c.Request.Context())
	if key == "" {
		status, response := h.processUpload(c, files, userID, db)
		return status, response, nil
	}
	if !idempotencyKeyPattern.MatchString(key) {
		return 0, models.ImageUploadResponse{}, errInvalidIdempotencyKey
	}
	hash, err := uploadFingerprint(files)
	if err != nil {
		return 0, models.ImageUploadResponse{}, err
	}
	var status int
	var response models.ImageUploadResponse
	err = db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Where("user_id = ? AND key = ? AND expires_at <= ?", userID, key, now).Delete(&models.UploadIdempotency{}).Error; err != nil {
			return err
		}
		entry := models.UploadIdempotency{UserID: userID, Key: key, RequestHash: hash, ExpiresAt: now.Add(24 * time.Hour)}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND key = ?", userID, key).First(&entry).Error; err != nil {
			return err
		}
		if entry.RequestHash != hash {
			return errIdempotencyConflict
		}
		if entry.StatusCode != 0 {
			status = entry.StatusCode
			return json.Unmarshal([]byte(entry.Response), &response)
		}
		status, response = h.processUpload(c, files, userID, tx)
		if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests {
			return errRetryableUpload
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return err
		}
		return tx.Model(&entry).Updates(map[string]any{"status_code": status, "response": string(encoded)}).Error
	})
	if errors.Is(err, errRetryableUpload) {
		return status, response, nil
	}
	return status, response, err
}
