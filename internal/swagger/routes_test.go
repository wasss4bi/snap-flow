package swagger_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"snap-flow-go/internal/api"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/swagger"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDocumentationRequiresCurrentAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.RevokedToken{}); err != nil {
		t.Fatal(err)
	}
	previousDB := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previousDB })

	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	admin := models.User{Name: "Admin", Email: "admin@example.test", Password: string(hash), IsAdmin: true, EmailVerifiedAt: &now}
	user := models.User{Name: "User", Email: "user@example.test", Password: string(hash), EmailVerifiedAt: &now}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	spec := filepath.Join(t.TempDir(), "openapi.yml")
	if err := os.WriteFile(spec, []byte("openapi: 3.1.2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := auth.NewJWTManager("test-secret")
	router := gin.New()
	handler := api.NewAuthHandler(manager)
	router.POST("/api/login", handler.Login)
	logout := router.Group("")
	logout.Use(auth.JWTMiddleware(manager))
	logout.POST("/logout", handler.Logout)
	swagger.RegisterRoutes(router, manager, spec)

	login := func(email string) *http.Cookie {
		t.Helper()
		body := fmt.Sprintf(`{"email":%q,"password":"test-password"}`, email)
		req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("login: %d %s", out.Code, out.Body.String())
		}
		for _, cookie := range out.Result().Cookies() {
			if cookie.Name == "auth_token" {
				return cookie
			}
		}
		t.Fatal("login did not set auth_token")
		return nil
	}
	adminCookie, userCookie := login(admin.Email), login(user.Email)
	request := func(path string, cookie *http.Cookie, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if got := out.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s cache-control = %q", path, got)
		}
		return out
	}
	for _, path := range []string{"/swagger/", "/swagger/index.html", "/swagger/swagger-ui.css", "/openapi.yml"} {
		wantAdmin := http.StatusOK
		if path == "/swagger/index.html" {
			wantAdmin = http.StatusMovedPermanently
		}
		if got := request(path, nil, "").Code; got != http.StatusUnauthorized {
			t.Errorf("anonymous %s: %d", path, got)
		}
		if got := request(path, userCookie, "").Code; got != http.StatusForbidden {
			t.Errorf("user %s: %d", path, got)
		}
		if got := request(path, adminCookie, "").Code; got != wantAdmin {
			t.Errorf("admin cookie %s: %d", path, got)
		}
		if got := request(path, nil, adminCookie.Value).Code; got != wantAdmin {
			t.Errorf("admin bearer %s: %d", path, got)
		}
	}
	if err := db.Model(&admin).Update("is_admin", false).Error; err != nil {
		t.Fatal(err)
	}
	if got := request("/openapi.yml", adminCookie, "").Code; got != http.StatusForbidden {
		t.Errorf("removed admin rights: %d", got)
	}
	if err := db.Model(&admin).Update("is_admin", true).Error; err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	req.AddCookie(adminCookie)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("logout: %d", out.Code)
	}
	if got := request("/swagger/", adminCookie, "").Code; got != http.StatusUnauthorized {
		t.Errorf("revoked cookie: %d", got)
	}
	if got := request("/openapi.yml", nil, adminCookie.Value).Code; got != http.StatusUnauthorized {
		t.Errorf("revoked bearer: %d", got)
	}
}
