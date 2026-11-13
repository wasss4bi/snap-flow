package queue

import (
	"context"
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"testing"
	"time"
)

func TestTerminalFailurePersistsStatusAndCleanup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.UploadOperation{}, &limits.UserLimit{}, &maintenance.Deletion{}); err != nil {
		t.Fatal(err)
	}
	old := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = old })
	if err = db.Create(&limits.UserLimit{UserID: 1, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 100, UsedBytes: 12}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&models.UploadOperation{Filename: "a.jpg", UserID: 1, State: "accepted", SizeBytes: 12}).Error; err != nil {
		t.Fatal(err)
	}
	task, err := NewImageProcessTask("a.jpg", "a.jpg", 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := HandleWithFailureTracking(func(context.Context, Task) error { return errors.Join(ErrPermanent, ErrInvalidImage) }, 3)
	if err = handler(context.Background(), task); !errors.Is(err, ErrPermanent) {
		t.Fatal(err)
	}
	var op models.UploadOperation
	if err = db.First(&op, "filename = ?", "a.jpg").Error; err != nil || op.State != "failed" || op.FailureCode != "invalid_image" || op.FailedAt == nil {
		t.Fatal("failure status missing", err)
	}
	var n int64
	db.Model(&maintenance.Deletion{}).Count(&n)
	if n != 1 {
		t.Fatal("cleanup not recorded")
	}
	var lim limits.UserLimit
	db.First(&lim, "user_id = ?", 1)
	if lim.UsedBytes != 12 {
		t.Fatal("quota released before cleanup")
	}
	if err := db.Create(&models.UploadOperation{Filename: "panic.jpg", UserID: 1, State: "accepted", SizeBytes: 1}).Error; err != nil {
		t.Fatal(err)
	}
	panicTask, err := NewImageProcessTask("panic.jpg", "panic.jpg", 1)
	if err != nil {
		t.Fatal(err)
	}
	panicHandler := HandleWithFailureTracking(func(context.Context, Task) error { panic("failure") }, 1)
	if err := panicHandler(context.Background(), panicTask); err == nil {
		t.Fatal("panic was not converted to an error")
	}
	var panicOp models.UploadOperation
	if err := db.First(&panicOp, "filename = ?", "panic.jpg").Error; err != nil || panicOp.State != "failed" {
		t.Fatal("panic status not persisted", err)
	}
}
