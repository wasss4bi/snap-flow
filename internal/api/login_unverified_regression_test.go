package api

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
)

func TestUnverifiedLoginDoesNotResetFailures(t *testing.T) {
	router, h := setupTestRouter()
	lim := &memoryLimiter{n: 4}
	h.SetLoginLimiter(lim)
	hash, err := bcrypt.GenerateFromPassword([]byte("Password123!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.User{Name: "User", Email: "unverified@example.test", Password: string(hash)}).Error; err != nil {
		t.Fatal(err)
	}
	router.POST("/login", h.Login)
	req := httptest.NewRequest("POST", "/login", bytes.NewBufferString(`{"email":"unverified@example.test","password":"Password123!"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 403 || lim.n != 4 {
		t.Fatalf("status=%d failures=%d; want 403 and 4", w.Code, lim.n)
	}
}
