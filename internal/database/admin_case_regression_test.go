package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/models"
)

func TestEnsureAdminNormalizesEmailForLogin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAdmin(db, config.AdminConfig{Name: "Admin", Email: "Admin@Example.test", Password: "Password123!"}); err != nil {
		t.Fatal(err)
	}
	var user models.User
	if err := db.Where("email = ?", "admin@example.test").First(&user).Error; err != nil {
		t.Fatalf("normalized login email not found: %v", err)
	}
}

func TestEnsureAdminRepairsExistingMixedCaseEmail(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	original := models.User{Name: "Existing", Email: "Admin@Example.test", Password: "original-hash", IsAdmin: true}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := EnsureAdmin(db, config.AdminConfig{Name: "Admin", Email: "Admin@Example.test", Password: "Password123!"}); err != nil {
		t.Fatal(err)
	}
	var found models.User
	if err := db.Where("email = ?", "admin@example.test").First(&found).Error; err != nil {
		t.Fatal(err)
	}
	if found.ID != original.ID || found.Password != original.Password {
		t.Fatalf("admin replaced: %+v", found)
	}
}

func TestEnsureAdminRejectsCaseInsensitiveCollision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.User{}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []models.User{{Name: "Admin", Email: "Admin@example.test", Password: "a", IsAdmin: true}, {Name: "Other", Email: "admin@example.test", Password: "b"}} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsureAdmin(db, config.AdminConfig{Name: "Admin", Email: "Admin@example.test", Password: "Password123!"}); err == nil {
		t.Fatal("ambiguous identity must fail")
	}
	var users []models.User
	if err := db.Order("id").Find(&users).Error; err != nil || len(users) != 2 || users[1].IsAdmin {
		t.Fatalf("users changed: %+v %v", users, err)
	}
}
