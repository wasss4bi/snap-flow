package api

import (
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"strings"
	"testing"
	"time"
)

func TestUploadStatusOwnedAndFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database.DB = setupTestDB()
	u := models.User{Name: "a", Email: "a@example.test", Password: "hash"}
	other := models.User{Name: "b", Email: "b@example.test", Password: "hash"}
	database.DB.Create(&u)
	database.DB.Create(&other)
	now := time.Now()
	database.DB.Create(&models.UploadOperation{Filename: "a.jpg", UserID: u.ID, State: "failed", FailureCode: "invalid_image", FailedAt: &now})
	current := u.ID
	router := gin.New()
	h := NewImageHandler(nil, nil)
	router.GET("/uploads/:filename", func(c *gin.Context) { c.Set("user_id", current); h.UploadStatus(c) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/uploads/a.jpg", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"failed"`) || !strings.Contains(w.Body.String(), "cannot be processed") {
		t.Fatal(w.Code, w.Body.String())
	}
	current = other.ID
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/uploads/a.jpg", nil))
	if w.Code != 404 {
		t.Fatal("other user's upload visible")
	}
}
