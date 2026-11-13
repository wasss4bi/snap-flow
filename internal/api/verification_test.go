package api

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"testing"
	"time"
)

func TestRegistrationConfirmationAndInvitation(t *testing.T) {
	router, h := setupTestRouter()
	router.POST("/register", h.Register)
	router.POST("/verify", h.VerifyEmail)
	router.POST("/login", h.Login)
	send := func(path string, v any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(v)
		req := httptest.NewRequest("POST", path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	_, secret, err := invite.Create(database.DB, time.Now().Add(time.Hour), 12345, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := models.RegisterRequest{Name: "User", Email: "user@example.test", Password: "Password123!", InviteCode: secret}
	if r := send("/register", req); r.Code != 201 {
		t.Fatalf("registration %d: %s", r.Code, r.Body.String())
	}
	req.Email = "other@example.test"
	if r := send("/register", req); r.Code != 400 {
		t.Fatal("invitation was reused")
	}
	var user models.User
	if err := database.DB.Where("email = ?", "user@example.test").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	var limit limits.UserLimit
	if err := database.DB.First(&limit, "user_id = ?", user.ID).Error; err != nil || limit.MemoryLimit != 12345 {
		t.Fatal("limits missing", err)
	}
	if r := send("/login", models.LoginRequest{Email: user.Email, Password: "Password123!"}); r.Code != 403 {
		t.Fatal("unverified login succeeded")
	}
	dispatchTestMail(t, h)
	code := h.mailer.(*testMailer).token
	if len(code) != 6 {
		t.Fatal("not a six-digit code")
	}
	if r := send("/verify", gin.H{"email": user.Email, "code": code}); r.Code != 200 {
		t.Fatalf("verify %d: %s", r.Code, r.Body.String())
	}
	if r := send("/login", models.LoginRequest{Email: user.Email, Password: "Password123!"}); r.Code != http.StatusOK {
		t.Fatal("verified login failed")
	}
}
