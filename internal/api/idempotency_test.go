package api

import (
	"bytes"
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

func TestUploadIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB()
	if err := db.AutoMigrate(&models.UploadIdempotency{}); err != nil {
		t.Fatal(err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	user := models.User{Name: "Owner", Email: "idempotency@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1000000}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	h := NewImageHandler(store, nil)
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) { c.Set("user_id", user.ID); h.UploadImages(c) })
	var img bytes.Buffer
	if err := jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 3, 3)), nil); err != nil {
		t.Fatal(err)
	}
	request := func(name string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		p, e := form.CreateFormFile("images", name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = p.Write(img.Bytes()); e != nil {
			t.Fatal(e)
		}
		if e = form.Close(); e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Idempotency-Key", "same-key")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	a, b := request("a.jpg"), request("a.jpg")
	if a.Code != 202 || b.Code != 202 || a.Body.String() != b.Body.String() {
		t.Fatalf("responses differ: %d %s / %d %s", a.Code, a.Body, b.Code, b.Body)
	}
	if c := request("other.jpg"); c.Code != http.StatusConflict {
		t.Fatalf("different request: %d", c.Code)
	}
	var count int64
	if err := db.Model(&models.UploadOperation{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("operations=%d error=%v", count, err)
	}
}
