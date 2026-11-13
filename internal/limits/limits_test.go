package limits

import (
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestReservationsEnforceAccessAndQuota(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&UserLimit{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&UserLimit{UserID: 1, AccessTo: now.Add(time.Hour), MemoryLimit: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := Reserve(db, 1, 7, now); err != nil {
		t.Fatal(err)
	}
	if err := Reserve(db, 1, 4, now); !errors.Is(err, ErrAccessOrQuota) {
		t.Fatal("quota exceeded")
	}
	if err := db.Model(&UserLimit{}).Where("user_id = ?", 1).Update("memory_limit", 5).Error; err != nil {
		t.Fatal(err)
	}
	if err := Reserve(db, 1, 1, now); !errors.Is(err, ErrAccessOrQuota) {
		t.Fatal("lowered quota ignored")
	}
	if err := Release(db, 1, 7); err != nil {
		t.Fatal(err)
	}
	if err := Reserve(db, 1, 1, now.Add(2*time.Hour)); !errors.Is(err, ErrAccessOrQuota) {
		t.Fatal("expired access ignored")
	}
}
