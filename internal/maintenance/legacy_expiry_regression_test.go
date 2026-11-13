package maintenance

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

func TestExpiryDeletesLegacyImageWithoutUploadOperation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.RevokedToken{}, &limits.UserLimit{}, &invite.Code{}, &Notice{}, &Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := models.User{Name: "Legacy", Email: "legacy@example.test", Password: "hash"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: u.ID, AccessTo: now.Add(-16 * 24 * time.Hour), MemoryLimit: 1000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UserImage{UserID: u.ID, Image: "legacy.jpg"}).Error; err != nil {
		t.Fatal(err)
	}
	historical := models.UserImage{UserID: u.ID, Image: "historical.jpg"}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&historical).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEncrypted([]byte("original"), "legacy.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreThumbnail([]byte("thumbnail"), "legacy.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEncrypted([]byte("original"), "historical.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreThumbnail([]byte("thumbnail"), "historical.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := Schedule(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if found, err := DispatchDeletion(context.Background(), db, store, now); err != nil || !found {
			t.Fatalf("legacy deletion missing: found=%v err=%v", found, err)
		}
	}
	if _, err := store.GetDecrypted("legacy.jpg"); err == nil {
		t.Fatal("original remains")
	}
	if _, err := store.GetDecryptedThumbnail("legacy.jpg"); err == nil {
		t.Fatal("thumbnail remains")
	}
	if _, err := store.GetDecrypted("historical.jpg"); err == nil {
		t.Fatal("previously hidden original remains")
	}
	if _, err := store.GetDecryptedThumbnail("historical.jpg"); err == nil {
		t.Fatal("previously hidden thumbnail remains")
	}
}
