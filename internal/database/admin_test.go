package database

import (
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/models"
	"testing"
)

func TestEnsureAdmin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	valid := config.AdminConfig{Name: "admin", Email: "admin@example.test", Password: "local-test-password"}
	for _, cfg := range []config.AdminConfig{{}, {Email: valid.Email, Password: valid.Password}, {Name: valid.Name, Password: valid.Password}, {Name: valid.Name, Email: valid.Email}, {Name: valid.Name, Email: "bad email", Password: valid.Password}} {
		if err := EnsureAdmin(db, cfg); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	if err := EnsureAdmin(db, valid); err != nil {
		t.Fatal(err)
	}
	var admin models.User
	if err := db.Where("email = ?", valid.Email).First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if !admin.IsAdmin || bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(valid.Password)) != nil {
		t.Fatal("administrator or password hash incorrect")
	}
	changed := valid
	changed.Password = "different-password"
	changed.Name = "different"
	if err := EnsureAdmin(db, changed); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.User{}).Where("email = ?", valid.Email).Count(&count)
	var again models.User
	db.First(&again, admin.ID)
	if count != 1 || again.Name != valid.Name || bcrypt.CompareHashAndPassword([]byte(again.Password), []byte(valid.Password)) != nil {
		t.Fatal("repeat altered administrator")
	}
	if err := db.Create(&models.User{Name: "Regular", Email: "regular@example.test", Password: "hash"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnsureAdmin(db, config.AdminConfig{Name: "admin", Email: "regular@example.test", Password: valid.Password}); err == nil {
		t.Fatal("regular user promoted")
	}
	var regular models.User
	db.Where("email = ?", "regular@example.test").First(&regular)
	if regular.IsAdmin {
		t.Fatal("regular user promoted")
	}
}
