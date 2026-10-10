package queue

import (
	"context"
	"encoding/json"
	"errors"
	"snap-flow-go/internal/logging"

	"snap-flow-go/internal/database"
	"snap-flow-go/internal/models"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/image"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TypeImageProcess = "image:process"
	TypeImageDelete  = "image:delete"
	TypeUserCleanup  = "user:cleanup"
	MessageVersion   = 2
	MaxMessageBytes  = 64 * 1024
)

// Task is the versioned application message, independent of the AMQP client.
// Attempt starts at zero; ID remains unchanged during retries.
type Task struct {
	RequestID string          `json:"request_id"`
	Version   int             `json:"version"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Attempt   int             `json:"attempt"`
	Payload   json.RawMessage `json:"payload"`
}

type Publisher interface {
	Publish(context.Context, Task) error
}

type ImageProcessPayload struct {
	StagingKey string `json:"staging_key"`
	Filename   string `json:"filename"`
	UserID     uint   `json:"user_id"`
}

type ImageDeletePayload struct {
	Filename string `json:"filename"`
}

type UserCleanupPayload struct {
	UserID uint `json:"user_id"`
}

func newTask(kind string, payload any) (Task, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Task{}, err
	}
	return Task{RequestID: uuid.NewString(), Version: MessageVersion, ID: uuid.NewString(), Type: kind, Payload: data}, nil
}

func NewImageProcessTask(key string, filename string, userID uint) (Task, error) {
	return newTask(TypeImageProcess, ImageProcessPayload{key, filename, userID})
}

func NewImageDeleteTask(filename string) (Task, error) {
	return newTask(TypeImageDelete, ImageDeletePayload{filename})
}

func NewUserCleanupTask(userID uint) (Task, error) {
	return newTask(TypeUserCleanup, UserCleanupPayload{userID})
}

var ErrPermanent = errors.New("permanent task error")
var ErrInvalidImage = errors.New("invalid image")
var ErrTerminalRecorded = errors.New("technical failure already recorded")

func permanent(err error) error { return errors.Join(ErrPermanent, err) }

func validateTask(task Task) error {
	if task.Version != MessageVersion || task.ID == "" || task.Attempt < 0 || len(task.Payload) == 0 {
		return errors.New("unsupported or malformed task envelope")
	}
	switch task.Type {
	case TypeImageProcess, TypeImageDelete, TypeUserCleanup:
		return nil
	default:
		return errors.New("unsupported task type")
	}
}

type TaskHandler struct{ storage storage.ImageStorage }

func NewTaskHandler(store storage.ImageStorage) *TaskHandler { return &TaskHandler{storage: store} }

func (h *TaskHandler) Handle(ctx context.Context, task Task) error {
	ctx = logging.WithRequestID(ctx, task.RequestID)
	switch task.Type {
	case TypeImageProcess:
		return h.HandleImageProcessTask(ctx, task)
	case TypeImageDelete:
		return h.HandleImageDeleteTask(ctx, task)
	case TypeUserCleanup:
		return h.HandleUserCleanupTask(ctx, task)
	default:
		return permanent(errors.New("unsupported task type"))
	}
}

func (h *TaskHandler) HandleImageProcessTask(ctx context.Context, task Task) error {
	var payload ImageProcessPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.UserID == 0 || payload.Filename == "" {
		return permanent(errors.New("invalid image process payload"))
	}
	stage, ok := h.storage.(storage.StagingStorage)
	if !ok {
		return errors.New("staging unavailable")
	}
	if payload.StagingKey != payload.Filename {
		return permanent(errors.New("invalid staging reference"))
	}
	err := database.GetDB().WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var op models.UploadOperation
		if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("filename = ? AND user_id = ?", payload.Filename, payload.UserID).First(&op).Error; err != nil {
			return err
		}
		if op.State == "deleted" {
			return h.storage.Delete(payload.Filename)
		}
		if op.State == "failed" && op.FailureCode != "invalid_image" {
			// A terminal status may have committed before parking was confirmed.
			return errors.Join(ErrPermanent, ErrTerminalRecorded)
		}
		if op.State == "deleting" || op.State == "failed" {
			return nil
		}
		if op.State == "complete" {
			return nil
		}
		if op.State != "accepted" {
			return permanent(errors.New("operation not accepted"))
		}
		data, err := stage.ReadStaged(payload.StagingKey)
		if err != nil {
			return err
		}
		original, thumbnail, err := image.ProcessImage(data, payload.Filename)
		if err != nil {
			if errors.Is(err, image.ErrInvalidImage) {
				return errors.Join(ErrPermanent, ErrInvalidImage, err)
			}
			return err
		}
		if err := h.storage.StoreEncrypted(original, payload.Filename); err != nil {
			return err
		}
		if err := h.storage.StoreThumbnail(thumbnail, payload.Filename); err != nil {
			return err
		}
		metadata := models.UserImage{UserID: payload.UserID, Image: payload.Filename}
		if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "image"}}, DoNothing: true}).Create(&metadata).Error; err != nil {
			return err
		}
		return db.Model(&op).Update("state", "complete").Error
	})
	if err != nil {
		return err
	}
	return stage.DeleteStaged(payload.StagingKey)
}

func (h *TaskHandler) HandleImageDeleteTask(ctx context.Context, task Task) error {
	var payload ImageDeletePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.Filename == "" {
		return permanent(errors.New("invalid image delete payload"))
	}
	if err := h.storage.Delete(payload.Filename); err != nil {
		return err
	}
	if stage, ok := h.storage.(storage.StagingStorage); ok {
		return stage.DeleteStaged(payload.Filename)
	}
	return nil
}

func (h *TaskHandler) HandleUserCleanupTask(ctx context.Context, task Task) error {
	var payload UserCleanupPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.UserID == 0 {
		return permanent(errors.New("invalid user cleanup payload"))
	}
	var images []models.UserImage
	if err := database.GetDB().WithContext(ctx).Where("user_id = ?", payload.UserID).Find(&images).Error; err != nil {
		return err
	}
	var failures []error
	for _, item := range images {
		if err := h.storage.Delete(item.Image); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
