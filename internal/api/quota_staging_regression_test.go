package api

import (
	"context"
	"testing"
	"time"

	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

func TestQuotaRejectionDoesNotWriteStaging(t *testing.T) {
	db := setupTestDB()
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	u := models.User{Name: "Owner", Email: "quota@example.test", Password: "hash"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: u.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewImageHandler(store, nil).accept(context.Background(), []byte("larger"), "quota.jpg", u.ID); err == nil {
		t.Fatal("expected quota rejection")
	}
	if _, err := store.ReadStaged("quota.jpg"); err == nil {
		t.Fatal("quota-rejected file remains in staging")
	}
}
