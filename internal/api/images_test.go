package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/queue"
	"time"

	"github.com/gin-gonic/gin"
)

type fakePublisher struct {
	fail      bool
	failAfter int
	calls     int
}

func (p *fakePublisher) Publish(ctx context.Context, task queue.Task) error {
	p.calls++
	if task.Version != 1 || ctx == nil {
		return errors.New("invalid publication contract")
	}
	if p.fail || (p.failAfter > 0 && p.calls > p.failAfter) {
		return errors.New("broker unavailable")
	}
	return nil
}

func TestUploadPublicationResults(t *testing.T) {
	for _, item := range []struct {
		name      string
		valid     bool
		files     int
		publisher fakePublisher
		status    int
		accepted  int
	}{
		{"accepted", true, 1, fakePublisher{}, 202, 1},
		{"all invalid", false, 1, fakePublisher{}, 400, 0},
		{"broker unavailable", true, 1, fakePublisher{fail: true}, 503, 0},
		{"partial publication", true, 2, fakePublisher{failAfter: 1}, 202, 2},
	} {
		t.Run(item.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			database.DB = setupTestDB()
			user := models.User{Name: "Uploader", Email: "upload@example.test", Password: "hash"}
			if err := database.DB.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			if err := database.DB.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 10000000}).Error; err != nil {
				t.Fatal(err)
			}
			var imageData bytes.Buffer
			if item.valid {
				if err := jpeg.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 20, 20)), nil); err != nil {
					t.Fatal(err)
				}
			} else {
				imageData.WriteString("invalid")
			}
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			for i := 0; i < item.files; i++ {
				part, err := form.CreateFormFile("images", "a.jpg")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = part.Write(imageData.Bytes()); err != nil {
					t.Fatal(err)
				}
			}
			if err := form.Close(); err != nil {
				t.Fatal(err)
			}
			handler := func() *ImageHandler {
				store, err := storage.NewLocalStorage(t.TempDir(), "local-test-key")
				if err != nil {
					t.Fatal(err)
				}
				if item.publisher.fail {
					database.DB.Callback().Create().Before("gorm:create").Register("fail_outbox", func(tx *gorm.DB) {
						if tx.Statement.Table == "outbox_events" {
							tx.AddError(errors.New("test database failure"))
						}
					})
				}
				return NewImageHandler(store, &item.publisher)
			}()
			router := gin.New()
			router.POST("/api/upload-images", func(c *gin.Context) { c.Set("user_id", user.ID); handler.UploadImages(c) })
			req := httptest.NewRequest(http.MethodPost, "/api/upload-images", &body)
			req.Header.Set("Content-Type", form.FormDataContentType())
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			var result models.ImageUploadResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != item.status || len(result.Files) != item.accepted || result.Success != (item.accepted > 0) {
				t.Fatalf("status=%d result=%+v", response.Code, result)
			}
		})
	}
}

func TestDeletePersistsCleanupWithoutBroker(t *testing.T) {
	database.DB = setupTestDB()
	user := models.User{Name: "Owner", Email: "delete@example.test", Password: "hash"}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	item := models.UserImage{UserID: user.ID, Image: "a.jpg"}
	if err := database.DB.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewImageHandler(nil, &fakePublisher{fail: true})
	router := gin.New()
	router.DELETE("/api/delete-images", func(c *gin.Context) { c.Set("user_id", user.ID); handler.DeleteAllImages(c) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/delete-images", nil))
	if response.Code != 200 {
		t.Fatalf("publication failure hidden: HTTP %d", response.Code)
	}
	var count int64
	if err := database.DB.Model(&models.UserImage{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("metadata remains visible")
	}
	var cleanup maintenance.Deletion
	if err := database.DB.First(&cleanup, "filename = ?", item.Image).Error; err != nil || cleanup.CompletedAt != nil {
		t.Fatal("cleanup not persisted", err)
	}
}

func TestDeleteAllIncludesUploadCompletedBetweenQueries(t *testing.T) {
	database.DB = setupTestDB()
	u := models.User{Name: "Owner", Email: "race@example.test", Password: "hash"}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	op := models.UploadOperation{Filename: "race.jpg", UserID: u.ID, State: "accepted"}
	if err := database.DB.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	fired := false
	if err := database.DB.Callback().Query().After("gorm:query").Register("complete_upload_between_reads", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "users_images" {
			return
		}
		fired = true
		db := tx.Session(&gorm.Session{NewDB: true})
		var current models.UploadOperation
		if err := db.First(&current, "filename = ?", op.Filename).Error; err != nil {
			t.Error(err)
			return
		}
		if current.State != "accepted" {
			return
		}
		if err := db.Create(&models.UserImage{UserID: u.ID, Image: op.Filename}).Error; err != nil {
			t.Error(err)
		}
		if err := db.Model(&op).Update("state", "complete").Error; err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	h := NewImageHandler(nil, nil)
	router := gin.New()
	router.DELETE("/images", func(c *gin.Context) { c.Set("user_id", u.ID); h.DeleteAllImages(c) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("DELETE", "/images", nil))
	if !fired {
		t.Fatal("upload completion was not attempted between the reads")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("delete failed: %d %s", w.Code, w.Body.String())
	}
	var count int64
	database.DB.Model(&models.UserImage{}).Where("user_id = ?", u.ID).Count(&count)
	if count != 0 {
		t.Errorf("completed image survived deletion (status %d)", w.Code)
	}
	var current models.UploadOperation
	if err := database.DB.First(&current, "filename = ?", op.Filename).Error; err != nil || current.State != "deleting" {
		t.Fatalf("upload was not fenced: state=%q err=%v", current.State, err)
	}
	var deletion maintenance.Deletion
	if err := database.DB.First(&deletion, "filename = ?", op.Filename).Error; err != nil {
		t.Fatalf("cleanup was not scheduled: %v", err)
	}
}

func TestDeleteAllSchedulesStoredAcceptedAndStaging(t *testing.T) {
	database.DB = setupTestDB()
	u := models.User{Name: "Owner", Email: "delete-states@example.test", Password: "hash"}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"staging", "accepted", "complete"} {
		op := models.UploadOperation{Filename: state + ".jpg", UserID: u.ID, State: state, SizeBytes: 12}
		if err := database.DB.Create(&op).Error; err != nil {
			t.Fatal(err)
		}
		if state == "complete" {
			if err := database.DB.Create(&models.UserImage{UserID: u.ID, Image: op.Filename}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	count, err := deleteAllTransaction(context.Background(), u.ID)
	if err != nil || count == 0 {
		t.Fatalf("delete failed: count=%d err=%v", count, err)
	}
	for _, state := range []string{"staging", "accepted", "complete"} {
		var op models.UploadOperation
		if err := database.DB.First(&op, "filename = ?", state+".jpg").Error; err != nil || op.State != "deleting" {
			t.Fatalf("%s state=%s err=%v", state, op.State, err)
		}
		if state == "staging" && op.SizeBytes != 0 {
			t.Fatal("unreserved staging must not release quota")
		}
		var deletion maintenance.Deletion
		if err := database.DB.First(&deletion, "filename = ?", op.Filename).Error; err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := database.DB.Model(&models.UserImage{}).Where("user_id = ?", u.ID).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("visible images: %d, %v", n, err)
	}
}
