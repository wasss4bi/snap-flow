package models

import (
	"gorm.io/gorm"
	"time"
)

type User struct {
	ID              uint           `gorm:"primaryKey" json:"id"`
	Name            string         `gorm:"not null" json:"name"`
	Email           string         `gorm:"unique;not null" json:"email"`
	Password        string         `gorm:"not null" json:"-"`
	EmailVerifiedAt *time.Time     `json:"email_verified_at,omitempty"`
	IsAdmin         bool           `gorm:"default:false" json:"is_admin"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`

	Images []UserImage `gorm:"foreignKey:UserID" json:"images,omitempty"`
}

type UserImage struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	UserID    uint           `gorm:"not null;index:idx_user_images_owner_image,priority:1" json:"user_id"`
	Image     string         `gorm:"not null;uniqueIndex:idx_user_images_filename;index:idx_user_images_owner_image,priority:2" json:"image"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName задаёт имя таблицы метаданных изображений.
func (UserImage) TableName() string {
	return "users_images"
}

// Структуры для API запросов/ответов
type RegisterRequest struct {
	Name       string `json:"name" binding:"required"`
	InviteCode string `json:"invite_code" binding:"required"`
	Email      string `json:"email" binding:"required,email"`
	Password   string `json:"password" binding:"required,min=6"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	Success           bool   `json:"success"`
	Token             string `json:"token,omitempty"`
	Message           string `json:"message,omitempty"`
	AttemptsRemaining int    `json:"attempts_remaining"`
}

type RevokedToken struct {
	Hash      string    `gorm:"primaryKey;size:64"`
	ExpiresAt time.Time `gorm:"not null;index"`
}

type UploadItem struct {
	OriginalName string `json:"original_name"`
	ID           string `json:"id,omitempty"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
}

type ImageUploadResponse struct {
	Success bool         `json:"success"`
	Files   []string     `json:"files,omitempty"`
	Uploads []UploadItem `json:"uploads,omitempty"`
	Message string       `json:"message,omitempty"`
}

type ImageCountResponse struct {
	Success     bool   `json:"success"`
	ImagesCount int    `json:"images_count,omitempty"`
	Error       string `json:"error,omitempty"`
}

type DeleteResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}
