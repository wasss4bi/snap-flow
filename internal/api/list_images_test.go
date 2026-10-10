package api

import (
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"testing"
)

func TestListImagesOwnerAndPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database.DB = setupTestDB()
	user := models.User{Name: "a", Email: "a@example.test", Password: "hash"}
	other := models.User{Name: "b", Email: "b@example.test", Password: "hash"}
	for _, u := range []*models.User{&user, &other} {
		if err := database.DB.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("%d.png", i)
		if err := database.DB.Create(&models.UserImage{UserID: user.ID, Image: name}).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.DB.Create(&models.UploadOperation{Filename: name, UserID: user.ID, State: "complete", SizeBytes: 10}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.DB.Create(&models.UserImage{UserID: other.ID, Image: "other.png"}).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	h := NewImageHandler(nil, nil)
	router.GET("/images", func(c *gin.Context) { c.Set("user_id", user.ID); h.ListImages(c) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/images?page=2&limit=2", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Images []ImageListItem `json:"images"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 3 || len(body.Images) != 1 || body.Images[0].SizeBytes != 10 {
		t.Fatalf("unexpected page %+v", body)
	}
}
