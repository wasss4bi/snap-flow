package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

func TestConcurrentImageLimitsPostgres(t *testing.T) {
	if os.Getenv("RUN_POSTGRES_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL")
	}
	cfg := config.Load().Database
	if cfg.Name != "snap_flow_test" {
		t.Fatal("requires snap_flow_test")
	}
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password), Host: cfg.Host + ":" + cfg.Port, Path: "/" + cfg.Name}
	q := u.Query()
	q.Set("sslmode", cfg.SSLMode)
	u.RawQuery = q.Encode()
	admin, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adminSQL.Close() }()
	schema := "snapflow_limits_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error }()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sqlDB.Close() }()
	if err := db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.OutboxEvent{}, &models.UploadIdempotency{}, &limits.UserLimit{}); err != nil {
		t.Fatal(err)
	}
	old := database.DB
	database.DB = db
	defer func() { database.DB = old }()
	store, err := storage.NewLocalStorage(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewImageHandler(store, nil)
	for _, tc := range []struct {
		name    string
		pending bool
		want    error
	}{{"pending", true, limits.ErrPendingImageLimit}, {"total", false, limits.ErrImageCountLimit}} {
		t.Run(tc.name, func(t *testing.T) {
			user := models.User{Name: "limits", Email: tc.name + "@example.test", Password: "hash"}
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1000000}).Error; err != nil {
				t.Fatal(err)
			}
			if tc.pending {
				ops := make([]models.UploadOperation, 99)
				for i := range ops {
					ops[i] = models.UploadOperation{UserID: user.ID, Filename: fmt.Sprintf("pending-%d.jpg", i), State: "accepted"}
				}
				if err := db.CreateInBatches(ops, 100).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				images := make([]models.UserImage, 999)
				for i := range images {
					images[i] = models.UserImage{UserID: user.ID, Image: fmt.Sprintf("image-%d.jpg", i)}
				}
				if err := db.CreateInBatches(images, 100).Error; err != nil {
					t.Fatal(err)
				}
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					results <- handler.accept(context.Background(), []byte("image"), fmt.Sprintf("%s-new-%d.jpg", tc.name, i), user.ID)
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			success, limited := 0, 0
			for err := range results {
				if err == nil {
					success++
				} else if errors.Is(err, tc.want) {
					limited++
				} else {
					t.Fatalf("unexpected result: %v", err)
				}
			}
			if success != 1 || limited != 1 {
				t.Fatalf("successful=%d limited=%d", success, limited)
			}
		})
	}
	t.Run("idempotency_concurrent", func(t *testing.T) {
		user := models.User{Name: "owner", Email: "idempotent-pg@example.test", Password: "hash"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1000000}).Error; err != nil {
			t.Fatal(err)
		}
		var img bytes.Buffer
		if err := jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
			t.Fatal(err)
		}
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.POST("/upload", func(c *gin.Context) { c.Set("user_id", user.ID); handler.UploadImages(c) })
		request := func() *httptest.ResponseRecorder {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			for _, name := range []string{"a.jpg", "b.jpg"} {
				part, e := w.CreateFormFile("images", name)
				if e != nil {
					panic(e)
				}
				if _, e = part.Write(img.Bytes()); e != nil {
					panic(e)
				}
			}
			if e := w.Close(); e != nil {
				panic(e)
			}
			req := httptest.NewRequest(http.MethodPost, "/upload", &body)
			req.Header.Set("Content-Type", w.FormDataContentType())
			req.Header.Set("Idempotency-Key", "concurrent-test")
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			return out
		}
		start := make(chan struct{})
		results := make(chan *httptest.ResponseRecorder, 2)
		for i := 0; i < 2; i++ {
			go func() { <-start; results <- request() }()
		}
		close(start)
		a, b := <-results, <-results
		if a.Code != 202 || b.Code != 202 || a.Body.String() != b.Body.String() {
			t.Fatalf("responses %d %s / %d %s", a.Code, a.Body, b.Code, b.Body)
		}
		var n int64
		if err := db.Model(&models.UploadOperation{}).Where("user_id = ?", user.ID).Count(&n).Error; err != nil || n != 2 {
			t.Fatalf("operations=%d error=%v", n, err)
		}
	})
	t.Run("staging_intent_survives_outer_rollback", func(t *testing.T) {
		user := models.User{Name: "owner", Email: "staging-rollback-pg@example.test", Password: "hash"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		limit := limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1000000}
		if err := db.Create(&limit).Error; err != nil {
			t.Fatal(err)
		}
		failure := errors.New("forced rollback")
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := handler.acceptDB(context.Background(), tx, []byte("image"), "rollback-staging.jpg", user.ID); err != nil {
				return err
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("transaction: %v", err)
		}
		var op models.UploadOperation
		if err := db.First(&op, "filename = ?", "rollback-staging.jpg").Error; err != nil || op.State != "staging" {
			t.Fatalf("staging intent: %+v %v", op, err)
		}
		if _, err := store.ReadStaged("rollback-staging.jpg"); err != nil {
			t.Fatalf("staged object: %v", err)
		}
		if err := db.First(&limit, "user_id = ?", user.ID).Error; err != nil || limit.UsedBytes != 0 {
			t.Fatalf("quota: %+v %v", limit, err)
		}
	})
}
