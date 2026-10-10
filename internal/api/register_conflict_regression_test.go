package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/invite"
)

func TestConcurrentRegistrationEmailConflictReturns409(t *testing.T) {
	router, h := setupTestRouter()
	router.POST("/register", h.Register)
	_, code, err := invite.Create(database.DB, time.Now().Add(time.Hour), 1000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Callback().Create().Before("gorm:create").Register("simulate_email_conflict", func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			_ = tx.AddError(&pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}) // Error is stored on tx.
		}
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"name": "User", "email": "race@example.test", "password": "Password123!", "invite_code": code})
	req := httptest.NewRequest("POST", "/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("email conflict: got %d %s, want 409", w.Code, w.Body.String())
	}
	var row invite.Code
	if err := database.DB.First(&row).Error; err != nil || row.UsedAt != nil {
		t.Fatalf("invite consumed on conflict: %+v %v", row, err)
	}
}
