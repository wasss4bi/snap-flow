package maintenance

import (
	"context"
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"testing"
	"time"
)

type fakeMail struct{ calls int }

func (m *fakeMail) SendExpiry(context.Context, string, time.Time) error {
	m.calls++
	if m.calls == 1 {
		return errors.New("temporary")
	}
	return nil
}
func TestExpiryRenewalDeletionAndRetry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.RevokedToken{}, &limits.UserLimit{}, &invite.Code{}, &Notice{}, &Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	user := models.User{Name: "test", Email: "test@example.test", Password: "hash"}
	if err = db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	access := now.Add(-16 * 24 * time.Hour)
	lim := limits.UserLimit{UserID: user.ID, AccessTo: access, MemoryLimit: 1000, UsedBytes: 100}
	if err = db.Create(&lim).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&models.UploadOperation{Filename: "a.jpg", UserID: user.ID, State: "complete", SizeBytes: 100}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&models.UserImage{UserID: user.ID, Image: "a.jpg"}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.StoreEncrypted([]byte("photo"), "a.jpg"); err != nil {
		t.Fatal(err)
	}
	// Renewal before the scheduler sees the deadline must preserve the image.
	if err = db.Model(&lim).Update("access_to", now.Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err = Schedule(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	var n int64
	db.Model(&models.UserImage{}).Count(&n)
	if n != 1 {
		t.Fatal("renewed image deleted")
	}
	if err = db.Model(&lim).Update("access_to", access).Error; err != nil {
		t.Fatal(err)
	}
	if err = Schedule(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	db.Model(&models.UserImage{}).Count(&n)
	if n != 0 {
		t.Fatal("expired image remains")
	}
	if err = db.First(&lim, "user_id = ?", user.ID).Error; err != nil || lim.UsedBytes != 100 {
		t.Fatal("quota released before physical deletion", err)
	}
	if found, err := DispatchDeletion(context.Background(), db, store, now); err != nil || !found {
		t.Fatal("deletion failed", err)
	}
	if err = db.First(&lim, "user_id = ?", user.ID).Error; err != nil || lim.UsedBytes != 0 {
		t.Fatal("quota not released after deletion", err)
	}
	if _, err = store.GetDecrypted("a.jpg"); err == nil {
		t.Fatal("physical image remains")
	}
	mail := &fakeMail{}
	if found, err := DispatchNotice(context.Background(), db, mail, now); err != nil || !found {
		t.Fatal(err)
	}
	if found, err := DispatchNotice(context.Background(), db, mail, now.Add(time.Minute)); err != nil || !found || mail.calls != 2 {
		t.Fatal("mail was not retried", err)
	}
}

func TestTechnicalFailureExpiresAndReleasesQuota(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.RevokedToken{}, &limits.UserLimit{}, &invite.Code{}, &Notice{}, &Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := now.Add(-8 * 24 * time.Hour)
	user := models.User{Name: "owner", Email: "expired-parking@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	limit := limits.UserLimit{UserID: user.ID, AccessTo: now.Add(time.Hour), MemoryLimit: 100, UsedBytes: 12}
	if err := db.Create(&limit).Error; err != nil {
		t.Fatal(err)
	}
	op := models.UploadOperation{Filename: "old.jpg", UserID: user.ID, State: "accepted", SizeBytes: 12, FirstFailedAt: &first}
	if err := db.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage([]byte("twelve bytes"), "old.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := Schedule(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	found, err := DispatchDeletion(context.Background(), db, store, now)
	if err != nil || !found {
		t.Fatalf("cleanup %v %v", found, err)
	}
	if err := db.First(&op, "filename = ?", "old.jpg").Error; err != nil || op.State != "failed" || op.SizeBytes != 0 {
		t.Fatalf("operation %+v %v", op, err)
	}
	if err := db.First(&limit, "user_id = ?", user.ID).Error; err != nil || limit.UsedBytes != 0 {
		t.Fatalf("quota %+v %v", limit, err)
	}
	if _, err := store.ReadStaged("old.jpg"); err == nil {
		t.Fatal("source not deleted")
	}
}

type flakyStorage struct {
	storage.ImageStorage
	failed bool
}

func (s *flakyStorage) Delete(name string) error {
	if !s.failed {
		s.failed = true
		return errors.New("storage unavailable")
	}
	return s.ImageStorage.Delete(name)
}
func TestDeletionRetainsQuotaUntilStorageSucceeds(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.UploadOperation{}, &limits.UserLimit{}, &Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	db.Create(&limits.UserLimit{UserID: 1, AccessTo: now, MemoryLimit: 100, UsedBytes: 20})
	db.Create(&models.UploadOperation{Filename: "photo.jpg", UserID: 1, State: "deleting", SizeBytes: 20})
	db.Create(&Deletion{Filename: "photo.jpg", AvailableAt: now})
	real, err := storage.NewLocalStorage(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if err = real.StoreEncrypted([]byte("test"), "photo.jpg"); err != nil {
		t.Fatal(err)
	}
	st := &flakyStorage{ImageStorage: real}
	found, err := DispatchDeletion(context.Background(), db, st, now)
	if err != nil || !found {
		t.Fatal(err)
	}
	var lim limits.UserLimit
	db.First(&lim, "user_id = ?", 1)
	if lim.UsedBytes != 20 {
		t.Fatal("released quota during failure")
	}
	found, err = DispatchDeletion(context.Background(), db, st, now.Add(time.Minute))
	if err != nil || !found {
		t.Fatal(err)
	}
	db.First(&lim, "user_id = ?", 1)
	if lim.UsedBytes != 0 {
		t.Fatal("quota not released after success")
	}
}
