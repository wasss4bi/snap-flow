package api

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/models"
	"testing"
	"time"
)

func TestLogoutRevokesOnlyCurrentTokenAndBearerPrecedence(t *testing.T) {
	router, h := setupTestRouter()
	now := time.Now()
	u := models.User{Name: "Owner", Email: "owner@example.test", Password: "hash", EmailVerifiedAt: &now}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	token, _ := h.jwtManager.GenerateToken(u.ID, u.Email, false)
	other, _ := h.jwtManager.GenerateToken(u.ID, u.Email, false)
	g := router.Group("")
	g.Use(auth.JWTMiddleware(h.jwtManager))
	g.GET("/protected", func(c *gin.Context) { c.Status(200) })
	g.POST("/logout", h.Logout)
	call := func(method, path, tok string, cookie bool) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "auth_token", Value: "invalid"})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if got := call("GET", "/protected", token, true); got != 200 {
		t.Errorf("bearer precedence: %d", got)
	}
	if got := call("POST", "/logout", token, false); got != 200 {
		t.Fatalf("logout: %d", got)
	}
	if got := call("GET", "/protected", token, false); got != 401 {
		t.Errorf("revoked token accepted: %d", got)
	}
	if token == other {
		t.Error("simultaneous logins must produce distinct tokens")
	} else if got := call("GET", "/protected", other, false); got != 200 {
		t.Errorf("other session rejected: %d", got)
	}
}

func TestRegistrationRejectsInvalidFieldsBeforeConsumingInvitation(t *testing.T) {
	router, h := setupTestRouter()
	router.POST("/register", h.Register)
	_, code, err := invite.Create(database.DB, time.Now().Add(time.Hour), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []models.RegisterRequest{{Name: "ab", Email: "a@example.test", Password: "Valid123!", InviteCode: code}, {Name: "User", Email: "a@example.test", Password: "password", InviteCode: code}, {Name: "User", Email: "a@example.test", Password: "Valid 123!", InviteCode: code}, {Name: "User Name", Email: "a@example.test", Password: "Valid123!", InviteCode: code}} {
		b, _ := json.Marshal(v)
		req := httptest.NewRequest("POST", "/register", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Errorf("invalid input accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
