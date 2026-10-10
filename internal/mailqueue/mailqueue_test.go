package mailqueue

import (
	"context"
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/models"
	"testing"
	"time"
)

type sender struct {
	calls int
	code  string
}

func (s *sender) SendVerification(_ context.Context, _ string, code string) error {
	s.calls++
	s.code = code
	if s.calls == 1 {
		return errors.New("smtp down")
	}
	return nil
}
func TestRetryAndExpire(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.User{}, &confirmation.Code{}, &ConfirmationMail{}); err != nil {
		t.Fatal(err)
	}
	u := models.User{Name: "user", Email: "user@example.test", Password: "hash"}
	if err = db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	code, err := confirmation.Issue(db, u.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = Enqueue(db, u.ID, u.Email, code, "secret"); err != nil {
		t.Fatal(err)
	}
	var row ConfirmationMail
	if err = db.First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if string(row.Ciphertext) == code {
		t.Fatal("plaintext code stored")
	}
	s := &sender{}
	firstDispatch := row.AvailableAt.Add(time.Second)
	found, err := Dispatch(context.Background(), db, s, "secret", firstDispatch)
	if err != nil || !found {
		t.Fatal(err)
	}
	found, err = Dispatch(context.Background(), db, s, "secret", firstDispatch.Add(2*time.Minute))
	if err != nil || !found || s.code != code {
		t.Fatal("retry failed", err)
	}
	if err = db.First(&row).Error; err != nil || row.SentAt == nil || len(row.Ciphertext) != 0 {
		t.Fatal("sent mail not cleaned", err)
	}
}
func TestSupersededCodeIsNotSent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.User{}, &confirmation.Code{}, &ConfirmationMail{}); err != nil {
		t.Fatal(err)
	}
	u := models.User{Name: "user", Email: "other@example.test", Password: "hash"}
	db.Create(&u)
	now := time.Now().UTC()
	first, err := confirmation.Issue(db, u.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = Enqueue(db, u.ID, u.Email, first, "secret"); err != nil {
		t.Fatal(err)
	}
	_, err = confirmation.Issue(db, u.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	s := &sender{}
	found, err := Dispatch(context.Background(), db, s, "secret", now.Add(2*time.Minute))
	if err != nil || !found || s.calls != 0 {
		t.Fatal("superseded code sent", err)
	}
	var mail ConfirmationMail
	db.First(&mail)
	if mail.CancelledAt == nil || len(mail.Ciphertext) != 0 {
		t.Fatal("superseded mail not cancelled")
	}
}
