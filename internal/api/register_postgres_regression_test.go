package api

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/mailqueue"
	"snap-flow-go/internal/models"
)

func TestConcurrentRegistrationPostgres(t *testing.T) {
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
	defer func() {
		if err := adminSQL.Close(); err != nil {
			t.Errorf("close adminSQL: %v", err)
		}
	}()
	schema := "snapflow_registration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
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
	defer func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close sqlDB: %v", err)
		}
	}()
	if err := db.AutoMigrate(&models.User{}, &invite.Code{}, &limits.UserLimit{}, &confirmation.Code{}, &mailqueue.ConfirmationMail{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	defer func() { database.DB = previous }()
	_, first, err := invite.Create(db, time.Now().Add(time.Hour), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := invite.Create(db, time.Now().Add(time.Hour), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var mutex sync.Mutex
	entered := 0
	ready := make(chan struct{})
	if err := db.Callback().Create().Before("gorm:create").Register("synchronize_user_insert", func(tx *gorm.DB) {
		if tx.Statement.Table != "users" {
			return
		}
		mutex.Lock()
		entered++
		if entered == 2 {
			close(ready)
		}
		mutex.Unlock()
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			_ = tx.AddError(fmt.Errorf("registration barrier timed out")) // Error is stored on tx.
		}
	}); err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(auth.NewJWTManager("test"))
	h.SetMailSecret("test")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/register", h.Register)
	statuses := make(chan int, 2)
	for _, code := range []string{first, second} {
		go func(code string) {
			body, _ := json.Marshal(map[string]string{"name": "User", "email": "same@example.test", "password": "Password123!", "invite_code": code})
			req := httptest.NewRequest("POST", "/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			statuses <- w.Code
		}(code)
	}
	a, b := <-statuses, <-statuses
	if (a != 201 || b != 409) && (a != 409 || b != 201) {
		t.Fatalf("statuses %d, %d", a, b)
	}
	var users, used int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&invite.Code{}).Where("used_at IS NOT NULL").Count(&used).Error; err != nil {
		t.Fatal(err)
	}
	if users != 1 || used != 1 {
		t.Fatalf("users=%d used invites=%d", users, used)
	}
}
