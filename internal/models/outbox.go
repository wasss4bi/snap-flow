package models

import "time"

type UploadOperation struct {
	Filename      string `gorm:"primaryKey;size:128"`
	UserID        uint   `gorm:"not null;index"`
	State         string `gorm:"not null;index;size:32"`
	RequestID     string `gorm:"size:64"`
	SizeBytes     int64  `gorm:"not null;default:0"`
	FailureCode   string `gorm:"size:64"`
	FailedAt      *time.Time
	FirstFailedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type OutboxEvent struct {
	ID          string `gorm:"primaryKey;size:64"`
	RequestID   string `gorm:"size:64;index"`
	Body        string `gorm:"type:text;not null"`
	Attempts    int
	AvailableAt time.Time  `gorm:"index"`
	PublishedAt *time.Time `gorm:"index"`
	Failed      bool       `gorm:"index"`
	CreatedAt   time.Time
}

type ParkingRecovery struct {
	TaskID    string `gorm:"primaryKey;size:64"`
	CreatedAt time.Time
}
