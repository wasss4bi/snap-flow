package api

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"snap-flow-go/internal/database"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/queue"
	"time"
)

var errCommitResponseLost = errors.New("commit response lost")
var registerCommitFaultDriver sync.Once
var commitFaultArmed atomic.Bool

type commitFaultDriver struct{ armed *atomic.Bool }

func (d commitFaultDriver) Open(name string) (driver.Conn, error) {
	conn, err := (&sqlite3.SQLiteDriver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &commitFaultConn{SQLiteConn: conn.(*sqlite3.SQLiteConn), armed: d.armed}, nil
}

type commitFaultConn struct {
	*sqlite3.SQLiteConn
	armed *atomic.Bool
}

func (c *commitFaultConn) Begin() (driver.Tx, error) {
	tx, err := c.SQLiteConn.Begin()
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, armed: c.armed}, nil
}

func (c *commitFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.SQLiteConn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, armed: c.armed}, nil
}

type commitFaultTx struct {
	driver.Tx
	armed *atomic.Bool
}

func (tx *commitFaultTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.armed.Swap(false) {
		return errCommitResponseLost
	}
	return nil
}

// Arm the fault after the initial reservation commits, so the next commit is the
// transaction containing accepted state and the outbox event.
type armOnStage struct {
	*storage.EncryptedStorage
	armed *atomic.Bool
}

func (s *armOnStage) Stage(data []byte, name string) error {
	if err := s.EncryptedStorage.Stage(data, name); err != nil {
		return err
	}
	s.armed.Store(true)
	return nil
}

func (s *armOnStage) StageContext(ctx context.Context, data []byte, name string) error {
	if err := s.EncryptedStorage.StageContext(ctx, data, name); err != nil {
		return err
	}
	s.armed.Store(true)
	return nil
}

func TestAcceptPreservesCommittedOperationWhenCommitResponseIsLost(t *testing.T) {
	commitFaultArmed.Store(false)
	registerCommitFaultDriver.Do(func() { sql.Register("sqlite3_commit_response_lost", commitFaultDriver{armed: &commitFaultArmed}) })
	db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite3_commit_response_lost", DSN: ":memory:"}, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.OutboxEvent{}, &limits.UserLimit{}); err != nil {
		t.Fatal(err)
	}
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	user := models.User{Name: "Owner", Email: "owner@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 10000000}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "local-test-key")
	if err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := jpeg.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 20, 20)), nil); err != nil {
		t.Fatal(err)
	}
	h := NewImageHandler(&armOnStage{EncryptedStorage: store, armed: &commitFaultArmed}, nil)
	const filename = "commit-unknown.jpg"
	if err := h.accept(context.Background(), imageData.Bytes(), filename, user.ID); !errors.Is(err, errCommitResponseLost) {
		t.Fatalf("expected lost commit acknowledgement, got %v", err)
	}
	var op models.UploadOperation
	if err := db.First(&op, "filename = ?", filename).Error; err != nil {
		t.Fatal(err)
	}
	if op.State != "accepted" {
		t.Fatalf("committed operation changed to %q", op.State)
	}
	var events []models.OutboxEvent
	if err := db.Find(&events).Error; err != nil || len(events) != 1 {
		t.Fatalf("outbox event missing: %v", err)
	}
	if staged, err := store.ReadStaged(filename); err != nil || !bytes.Equal(staged, imageData.Bytes()) {
		t.Fatalf("staged file lost: %v", err)
	}
	var task queue.Task
	if err := json.Unmarshal([]byte(events[0].Body), &task); err != nil {
		t.Fatal(err)
	}
	worker := queue.NewTaskHandler(store)
	for i := 0; i < 2; i++ {
		if err := worker.Handle(context.Background(), task); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	var count int64
	if err := db.Model(&models.UserImage{}).Where("image = ?", filename).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("metadata count %d: %v", count, err)
	}
	if original, err := store.GetDecrypted(filename); err != nil || !bytes.Equal(original, imageData.Bytes()) {
		t.Fatalf("original not available: %v", err)
	}
	if _, err := store.ReadStaged(filename); err == nil {
		t.Fatal("completed staging was not removed")
	}
}

func TestAcceptMarksRolledBackReservationAbandoned(t *testing.T) {
	db := setupTestDB()
	previous := database.DB
	database.DB = db
	t.Cleanup(func() { database.DB = previous })
	user := models.User{Name: "Owner", Email: "rollback@example.test", Password: "hash"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("fail_outbox_record", func(tx *gorm.DB) {
		if tx.Statement.Table == "outbox_events" {
			tx.AddError(errors.New("outbox failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&limits.UserLimit{UserID: user.ID, AccessTo: time.Now().Add(time.Hour), MemoryLimit: 10000000}).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "local-test-key")
	if err != nil {
		t.Fatal(err)
	}
	h := NewImageHandler(store, nil)
	if err := h.accept(context.Background(), []byte("test data"), "rollback.jpg", user.ID); err == nil {
		t.Fatal("transaction should fail")
	}
	var op models.UploadOperation
	if err := db.First(&op, "filename = ?", "rollback.jpg").Error; err != nil || op.State != "abandoned" {
		t.Fatalf("reservation state %q: %v", op.State, err)
	}
	var count int64
	if err := db.Model(&models.OutboxEvent{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled back outbox count %d: %v", count, err)
	}
	if _, err := store.ReadStaged("rollback.jpg"); err != nil {
		t.Fatalf("staged file should remain available for cleanup: %v", err)
	}
}
