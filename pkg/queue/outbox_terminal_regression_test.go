package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
)

type alwaysFailPublisher struct {
	parkingAvailable bool
	publishCalls     *int
}

func (p alwaysFailPublisher) Publish(context.Context, Task) error {
	if p.publishCalls != nil {
		(*p.publishCalls)++
	}
	return errors.New("unavailable")
}
func (p alwaysFailPublisher) PublishFailure(context.Context, []byte, string) error {
	if !p.parkingAvailable {
		return errors.New("parking unavailable")
	}
	return nil
}

func TestTerminalOutboxFailureRetainsSourceUntilParkingIsAvailable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.UploadOperation{}, &models.OutboxEvent{}, &limits.UserLimit{}, &maintenance.Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Create(&limits.UserLimit{UserID: 1, AccessTo: now.Add(time.Hour), MemoryLimit: 100, UsedBytes: 12}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UploadOperation{Filename: "terminal.jpg", UserID: 1, State: "accepted", SizeBytes: 12}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Stage([]byte("source"), "terminal.jpg"); err != nil {
		t.Fatal(err)
	}
	task, err := NewImageProcessTask("terminal.jpg", "terminal.jpg", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(db, task, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.OutboxEvent{}).Where("id = ?", task.ID).Update("attempts", 5).Error; err != nil {
		t.Fatal(err)
	}
	var calls int
	if found, err := DispatchOnce(context.Background(), db, alwaysFailPublisher{publishCalls: &calls}); err != nil || !found {
		t.Fatalf("dispatch: %v %v", found, err)
	}
	var event models.OutboxEvent
	var op models.UploadOperation
	if err := db.First(&event, "id = ?", task.ID).Error; err != nil || event.Failed || event.Attempts != 6 {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if err := db.First(&op, "filename = ?", "terminal.jpg").Error; err != nil || op.State != "failed" || op.FailureCode != "queue_unavailable" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	if err := db.Model(&event).Update("available_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if found, err := DispatchOnce(context.Background(), db, alwaysFailPublisher{parkingAvailable: true, publishCalls: &calls}); err != nil || !found {
		t.Fatalf("parking: %v %v", found, err)
	}
	if calls != 1 {
		t.Fatalf("terminal event was republished to work queue: %d", calls)
	}
	if err := db.First(&event, "id = ?", task.ID).Error; err != nil || !event.Failed {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if err := db.First(&op, "filename = ?", "terminal.jpg").Error; err != nil || op.State != "failed" || op.FailureCode != "queue_unavailable" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	if found, err := maintenance.DispatchDeletion(context.Background(), db, store, time.Now().Add(time.Second)); err != nil || found {
		t.Fatalf("technical failure should not schedule cleanup: %v %v", found, err)
	}
	if _, err := store.ReadStaged("terminal.jpg"); err != nil {
		t.Fatal("source lost", err)
	}
	var lim limits.UserLimit
	if err := db.First(&lim, "user_id = ?", 1).Error; err != nil || lim.UsedBytes != 12 {
		t.Fatalf("quota=%+v err=%v", lim, err)
	}
}
