package confirmation

import (
	"errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestConfirmationLimitAndResend(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Code{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	code, err := Issue(db, 1, now)
	if err != nil || len(code) != 6 {
		t.Fatal("code generation failed", err)
	}
	if _, err := Issue(db, 1, now); !errors.Is(err, ErrCooldown) {
		t.Fatal("cooldown missing")
	}
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for i := 0; i < 5; i++ {
		ok, err := Check(db, 1, wrong, now)
		if err != nil || ok {
			t.Fatal("wrong code accepted")
		}
	}
	if ok, err := Check(db, 1, code, now); err != nil || ok {
		t.Fatal("attempt limit ignored")
	}
	next, err := Issue(db, 1, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := Check(db, 1, next, now.Add(time.Minute)); err != nil || !ok {
		t.Fatal("new code rejected")
	}
	if ok, err := Check(db, 1, next, now.Add(11*time.Minute)); err != nil || ok {
		t.Fatal("expired code accepted")
	}
}
