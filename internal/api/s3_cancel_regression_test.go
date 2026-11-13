package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

func TestCancelledUploadStopsStalledS3Stage(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
			w.WriteHeader(200)
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	db := setupTestDB()
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	u := models.User{Name: "User", Email: "s3@example.test", Password: "hash"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: u.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewS3Storage(&config.Config{Storage: config.StorageConfig{Endpoint: srv.URL, Region: "test", Bucket: "test", AccessKey: "test", SecretKey: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewImageHandler(store, nil).accept(ctx, []byte("image"), "test.jpg", u.ID) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		cancel()
		close(release)
		t.Fatal("S3 request not received")
	}
	cancel()
	var stopped bool
	select {
	case <-done:
		stopped = true
	case <-time.After(time.Second):
	}
	close(release)
	if !stopped {
		<-done
		t.Fatal("S3 request ignored upload cancellation")
	}
}
