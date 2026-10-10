package models

import "time"

type UploadIdempotency struct {
	UserID      uint      `gorm:"primaryKey"`
	Key         string    `gorm:"primaryKey;size:128"`
	RequestHash string    `gorm:"size:64;not null"`
	StatusCode  int       `gorm:"not null"`
	Response    string    `gorm:"type:text;not null"`
	ExpiresAt   time.Time `gorm:"not null;index"`
}

func (UploadIdempotency) TableName() string { return "upload_idempotency" }
