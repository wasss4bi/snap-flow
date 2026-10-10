package limits

import (
	"errors"
	"fmt"
	"testing"

	"snap-flow-go/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestImageCountLimitsIncludeLegacyAndPendingWithoutDoubleCounting(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}); err != nil {
		t.Fatal(err)
	}
	images := make([]models.UserImage, 999)
	for i := range images {
		images[i] = models.UserImage{UserID: 1, Image: fmt.Sprintf("%04d.jpg", i)}
	}
	if err := db.CreateInBatches(images, 100).Error; err != nil {
		t.Fatal(err)
	}
	// A complete operation with matching image must not be counted twice.
	if err := db.Create(&models.UploadOperation{Filename: "0000.jpg", UserID: 1, State: "complete"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := CheckImageCounts(db, 1); err != nil {
		t.Fatalf("999 images rejected: %v", err)
	}
	if err := db.Create(&models.UploadOperation{Filename: "pending.jpg", UserID: 1, State: "accepted"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := CheckImageCounts(db, 1); !errors.Is(err, ErrImageCountLimit) {
		t.Fatalf("1000 images: %v", err)
	}
	if err := db.Where("user_id = ?", 1).Delete(&models.UploadOperation{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("user_id = ?", 1).Delete(&models.UserImage{}).Error; err != nil {
		t.Fatal(err)
	}
	operations := make([]models.UploadOperation, MaxPendingImages)
	for i := range operations {
		operations[i] = models.UploadOperation{Filename: fmt.Sprintf("pending-%d.jpg", i), UserID: 1, State: "accepted"}
	}
	if err := db.CreateInBatches(operations, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := CheckImageCounts(db, 1); !errors.Is(err, ErrPendingImageLimit) {
		t.Fatalf("100 pending: %v", err)
	}
	if err := CheckImageCounts(db, 2); err != nil {
		t.Fatalf("other user's uploads counted: %v", err)
	}
}
