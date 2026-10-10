package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRepeatedRealProcessingWithPostgresAndRabbit(t *testing.T) {
	if os.Getenv("RUN_POSTGRES_TESTS") != "1" || os.Getenv("RUN_RABBIT_TESTS") != "1" {
		t.Skip("requires isolated PostgreSQL and RabbitMQ")
	}
	cfg := config.Load()
	if cfg.Database.Name != "snap_flow_test" {
		t.Fatal("requires dedicated test database")
	}
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(cfg.Database.User, cfg.Database.Password), Host: net.JoinHostPort(cfg.Database.Host, cfg.Database.Port), Path: "/" + cfg.Database.Name}
	q := u.Query()
	q.Set("sslmode", cfg.Database.SSLMode)
	u.RawQuery = q.Encode()
	admin, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adminSQL.Close() })
	schema := "snapflow_tasks_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	database.DB = db
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	owner := models.User{Name: "Queue owner", Email: "queue@example.test", Password: "hash"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalStorage(t.TempDir(), "queue-local-test-key")
	if err != nil {
		t.Fatal(err)
	}
	cfg.RabbitMQ.QueuePrefix = "snapflow.processing.test." + uuid.NewString()
	cfg.RabbitMQ.Prefetch = 4
	cfg.RabbitMQ.RetryDelaysMS = []int{100, 100}
	publisher, err := NewRabbitPublisher(cfg.RabbitMQ)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	conn, err := dialBroker(cfg.RabbitMQ, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := declareQueues(ch, cfg.RabbitMQ); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, suffix := range []string{"tasks", "retry", "parking"} {
			_, _ = ch.QueueDelete(cfg.RabbitMQ.QueuePrefix+"."+suffix, false, false, false)
		}
		for i := range cfg.RabbitMQ.RetryDelaysMS {
			_, _ = ch.QueueDelete(retryQueue(cfg.RabbitMQ.QueuePrefix, i), false, false, false)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
	results := make(chan error, 16)
	handler := NewTaskHandler(store)
	technicalName := "technical-failure.jpg"
	tracked := HandleWithFailureTracking(func(ctx context.Context, task Task) error {
		var payload ImageProcessPayload
		if task.Type == TypeImageProcess && json.Unmarshal(task.Payload, &payload) == nil && payload.Filename == technicalName {
			return errors.New("storage unavailable")
		}
		return handler.Handle(ctx, task)
	}, publisher.MaxAttempts())
	go func() {
		defer close(done)
		_ = RunWorker(ctx, publisher, func(ctx context.Context, task Task) error {
			err := tracked(ctx, task)
			results <- err
			return err
		})
	}()
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 400, 250)), nil); err != nil {
		t.Fatal(err)
	}
	filename := uuid.NewString() + ".jpg"
	if err := store.Stage(data.Bytes(), filename); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UploadOperation{Filename: filename, UserID: owner.ID, State: "accepted"}).Error; err != nil {
		t.Fatal(err)
	}
	task, err := NewImageProcessTask(filename, filename, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var payload ImageProcessPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := publisher.Publish(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 8; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("repeated processing timed out")
		}
	}
	var count int64
	if err := db.Unscoped().Model(&models.UserImage{}).Where("image = ?", payload.Filename).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate metadata: count=%d error=%v", count, err)
	}
	plain, err := store.GetDecrypted(payload.Filename)
	if err != nil || !bytes.Equal(plain, data.Bytes()) {
		t.Fatalf("original changed during replay: %v", err)
	}
	thumbnail, err := store.GetDecryptedThumbnail(payload.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(thumbnail)); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.UploadOperation{}).Where("filename = ?", payload.Filename).Update("state", "deleted").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("image = ?", payload.Filename).Delete(&models.UserImage{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("deleted operation replay timed out")
	}
	if _, err := store.GetDecrypted(payload.Filename); err == nil {
		t.Fatal("deleted image was resurrected")
	}

	// A failed thumbnail leaves the source available for the next full attempt.
	partialName := uuid.NewString() + ".jpg"
	if err := store.Stage(data.Bytes(), partialName); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UploadOperation{Filename: partialName, UserID: owner.ID, State: "accepted"}).Error; err != nil {
		t.Fatal(err)
	}
	partialTask, _ := NewImageProcessTask(partialName, partialName, owner.ID)
	partial := &thumbnailFailure{ImageStorage: store, StagingStorage: store, fail: true}
	if err := NewTaskHandler(partial).Handle(context.Background(), partialTask); err == nil {
		t.Fatal("partial write did not fail")
	}
	if _, err := store.ReadStaged(partialName); err != nil {
		t.Fatal("source removed after partial failure")
	}
	partial.fail = false
	if err := NewTaskHandler(partial).Handle(context.Background(), partialTask); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadStaged(partialName); err == nil {
		t.Fatal("completed source was retained")
	}
	// Exhausted infrastructure failures are recorded and parked with source intact.
	if err := store.Stage(data.Bytes(), technicalName); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.UploadOperation{Filename: technicalName, UserID: owner.ID, State: "accepted", SizeBytes: int64(data.Len())}).Error; err != nil {
		t.Fatal(err)
	}
	technicalTask, err := NewImageProcessTask(technicalName, technicalName, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), technicalTask); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < publisher.MaxAttempts(); i++ {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("technical failure unexpectedly succeeded")
			}
		case <-time.After(20 * time.Second):
			t.Fatal("technical retries timed out")
		}
	}
	var failedOp models.UploadOperation
	if err := db.First(&failedOp, "filename = ?", technicalName).Error; err != nil || failedOp.State != "failed" || failedOp.FailureCode != "processing_failed" {
		t.Fatalf("failed operation=%+v err=%v", failedOp, err)
	}
	if _, err := store.ReadStaged(technicalName); err != nil {
		t.Fatalf("technical source was removed: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := ch.QueueDeclarePassive(cfg.RabbitMQ.QueuePrefix+".parking", true, false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if state.Messages == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("technical failure was not parked")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Tracked abandoned staging is collected; accepted operations retain their source.
	for _, state := range []string{"abandoned", "accepted"} {
		name := state + ".jpg"
		if err := store.Stage(data.Bytes(), name); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.UploadOperation{Filename: name, UserID: owner.ID, State: state, UpdatedAt: time.Now().Add(-2 * time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := CollectStaging(context.Background(), db, store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadStaged("abandoned.jpg"); err == nil {
		t.Fatal("abandoned stage not cleaned")
	}
	if _, err := store.ReadStaged("accepted.jpg"); err != nil {
		t.Fatal("accepted stage was prematurely collected")
	}
	// Rollback, SKIP LOCKED competing dispatchers, uncertain mark, bounded failure.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := Record(tx, task, "trace-outbox-test"); err != nil {
			return err
		}
		return errors.New("forced rollback")
	}); err == nil {
		t.Fatal("rollback missing")
	}
	var events int64
	db.Model(&models.OutboxEvent{}).Count(&events)
	if events != 0 {
		t.Fatal("rolled back event survived")
	}
	if err := Record(db, task, "trace-outbox-test"); err != nil {
		t.Fatal(err)
	}
	var published atomic.Int32
	fake := publisherFunc(func(_ context.Context, msg Task) error {
		if msg.RequestID != "trace-outbox-test" {
			t.Error("request ID lost")
		}
		if bytes.Contains(msg.Payload, []byte("image_data")) {
			t.Error("inline bytes survived")
		}
		published.Add(1)
		time.Sleep(50 * time.Millisecond)
		return nil
	})
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := DispatchOnce(context.Background(), db, fake); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if published.Load() != 1 {
		t.Fatalf("competing publishers: %d", published.Load())
	}
	// Simulate a crash after broker confirmation, before marking the row.
	db.Model(&models.OutboxEvent{}).Where("id = ?", task.ID).Update("published_at", nil)
	if _, err := DispatchOnce(context.Background(), db, fake); err != nil {
		t.Fatal(err)
	}
	if published.Load() != 2 {
		t.Fatal("uncertain publication was not repeated")
	}
	db.Model(&models.OutboxEvent{}).Where("id = ?", task.ID).Updates(map[string]any{"published_at": nil, "attempts": 19})
	if _, err := DispatchOnce(context.Background(), db, publisherFunc(func(context.Context, Task) error { return errors.New("broker offline") })); err != nil {
		t.Fatal(err)
	}
	var failed models.OutboxEvent
	if err := db.First(&failed, "id = ?", task.ID).Error; err != nil || failed.Failed || failed.PublishedAt != nil {
		t.Fatal("unavailable broker must leave the outbox event pending")
	}
	if found, err := DispatchOnce(context.Background(), db, fake); err != nil || found {
		t.Fatal("outbox retry delay was skipped")
	}
	oldFailure := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&models.UploadOperation{}).Where("filename = ?", technicalName).Updates(map[string]any{"failed_at": oldFailure, "first_failed_at": oldFailure}).Error; err != nil {
		t.Fatal(err)
	}
	if err := recoverOneParked(context.Background(), publisher); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&failedOp, "filename = ?", technicalName).Error; err != nil || failedOp.State != "accepted" {
		t.Fatalf("parking recovery: %+v %v", failedOp, err)
	}
	var recovered int64
	if err := db.Model(&models.ParkingRecovery{}).Count(&recovered).Error; err != nil || recovered != 1 {
		t.Fatalf("recovery records %d: %v", recovered, err)
	}
	t.Log("outbox rollback, parallel SKIP LOCKED publishers, duplicate after unmarked confirmation and pending broker failure passed")
	t.Log("8 concurrent RabbitMQ deliveries of one task produced one PostgreSQL row and intact encrypted files; replay after soft delete did not resurrect the object")
}

type publisherFunc func(context.Context, Task) error

func (f publisherFunc) Publish(ctx context.Context, t Task) error { return f(ctx, t) }

type thumbnailFailure struct {
	storage.ImageStorage
	storage.StagingStorage
	fail bool
}

func (s *thumbnailFailure) StoreThumbnail(data []byte, name string) error {
	if s.fail {
		return errors.New("temporary thumbnail write failure")
	}
	return s.ImageStorage.StoreThumbnail(data, name)
}
