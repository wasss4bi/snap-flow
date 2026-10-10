package queue

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"
)

func TestParkingRecoveryIsDurableAndDeduplicated(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.UploadOperation{}, &models.OutboxEvent{}, &models.ParkingRecovery{}, &maintenance.Deletion{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := now.Add(-2 * time.Hour)
	op := models.UploadOperation{Filename: "a.jpg", UserID: 1, State: "failed", FailureCode: "processing_failed", SizeBytes: 12, FailedAt: &first, FirstFailedAt: &first}
	if err := db.Create(&op).Error; err != nil {
		t.Fatal(err)
	}
	task, err := NewImageProcessTask("a.jpg", "a.jpg", 1)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := recoverParked(context.Background(), db, task, now)
	if err != nil || !ack {
		t.Fatalf("recovery: %v %v", ack, err)
	}
	ack, err = recoverParked(context.Background(), db, task, now)
	if err != nil || !ack {
		t.Fatalf("duplicate: %v %v", ack, err)
	}
	var n int64
	if err := db.Model(&models.OutboxEvent{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("events=%d err=%v", n, err)
	}
	if err := db.First(&op, "filename = ?", "a.jpg").Error; err != nil || op.State != "accepted" || op.FirstFailedAt == nil {
		t.Fatalf("operation=%+v error=%v", op, err)
	}
	tooOld := now.Add(-8 * 24 * time.Hour)
	tooRecent := now.Add(-time.Minute)
	for _, tc := range []struct {
		filename    string
		first, last time.Time
	}{
		{"expired.jpg", tooOld, first}, {"recent.jpg", first, tooRecent},
	} {
		item := models.UploadOperation{Filename: tc.filename, UserID: 1, State: "failed", FailureCode: "processing_failed", SizeBytes: 12, FirstFailedAt: &tc.first, FailedAt: &tc.last}
		if err := db.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		msg, err := NewImageProcessTask(tc.filename, tc.filename, 1)
		if err != nil {
			t.Fatal(err)
		}
		ack, err := recoverParked(context.Background(), db, msg, now)
		if err != nil || ack {
			t.Fatalf("%s unexpectedly recovered: %v %v", tc.filename, ack, err)
		}
	}
	legacy := models.UploadOperation{Filename: "unknown.jpg", UserID: 1, State: "failed", FailureCode: "processing_failed", FirstFailedAt: &first, FailedAt: &first}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	legacyTask, err := NewImageProcessTask("unknown.jpg", "unknown.jpg", 1)
	if err != nil {
		t.Fatal(err)
	}
	ack, err = recoverParked(context.Background(), db, legacyTask, now)
	if err != nil || ack {
		t.Fatalf("unknown legacy data discarded: %v %v", ack, err)
	}
	if err := db.Create(&maintenance.Deletion{Filename: "unknown.jpg", AvailableAt: now, CompletedAt: &now}).Error; err != nil {
		t.Fatal(err)
	}
	ack, err = recoverParked(context.Background(), db, legacyTask, now)
	if err != nil || !ack {
		t.Fatalf("cleaned task retained: %v %v", ack, err)
	}
}
