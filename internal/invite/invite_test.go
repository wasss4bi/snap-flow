package invite

import (
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
	"time"
)

func TestLifecycleAndRollback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&Code{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	row, secret, err := Create(db, now.Add(48*time.Hour), 1234, now)
	if err != nil {
		t.Fatal(err)
	}
	if row.CodeHash == secret || row.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatal("invalid stored invitation")
	}
	rollback := errors.New("rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := Consume(tx, secret, now); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { _, err := Consume(tx, secret, now); return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := Consume(db, secret, now); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reused code: %v", err)
	}
	_, expired, err := Create(db, now.Add(48*time.Hour), 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Consume(db, expired, now.Add(24*time.Hour)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expired code: %v", err)
	}
	if err := DeleteExpiredUnused(db, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&Code{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("used history not retained: %d %v", count, err)
	}
}
