package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

func TestUploadRequestAndPendingBoundaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB()
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	user := models.User{Name: "Owner", Email: "limits-upload@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 10_000_000}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	h := NewImageHandler(store, nil)
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) { c.Set("user_id", user.ID); h.UploadImages(c) })
	var imageData bytes.Buffer
	if err := jpeg.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 10, 10)), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.UploadIdempotency{}); err != nil {
		t.Fatal(err)
	}
	request := func(count int, key string) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for i := 0; i < count; i++ {
			part, err := form.CreateFormFile("images", "a.jpg")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(imageData.Bytes()); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		r := httptest.NewRecorder()
		router.ServeHTTP(r, req)
		return r
	}
	if got := request(101, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("101 files: %d", got.Code)
	}
	got := request(100, "")
	var response models.ImageUploadResponse
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if got.Code != http.StatusAccepted || len(response.Files) != 100 {
		t.Fatalf("100 files: status=%d accepted=%d", got.Code, len(response.Files))
	}
	if got := request(1, "retry-pending"); got.Code != http.StatusTooManyRequests {
		t.Fatalf("101st pending file: %d", got.Code)
	}
	if err := db.Model(&models.UploadOperation{}).Where("state = ?", "accepted").Update("state", "complete").Error; err != nil {
		t.Fatal(err)
	}
	if got := request(1, "retry-pending"); got.Code != http.StatusAccepted {
		t.Fatalf("retry after capacity frees: %d %s", got.Code, got.Body)
	}
}
