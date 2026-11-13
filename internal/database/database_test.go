package database

import (
	"net/url"
	"os"
	"strings"
	"testing"

	"snap-flow-go/internal/config"
	"snap-flow-go/internal/confirmation"
	"snap-flow-go/internal/invite"
	"snap-flow-go/internal/limits"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/models"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresDSNEscapesCredentials(t *testing.T) {
	cfg := config.DatabaseConfig{Host: "::1", Port: "5432", Name: "snap_flow", User: "name@example", Password: "p@ss word:'/?&", SSLMode: "require"}
	u, err := url.Parse(postgresDSN(cfg))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Host != "[::1]:5432" || u.User.Username() != cfg.User || password != cfg.Password || u.Query().Get("sslmode") != "require" || u.Query().Get("timezone") != "UTC" {
		t.Fatal("DSN did not preserve connection settings")
	}
}

// Enabled by the tests Compose service against its own fresh test database.
// Every run owns one unique schema and removes only that schema.
func TestPostgresSchemaAndQueries(t *testing.T) {
	if os.Getenv("RUN_POSTGRES_TESTS") != "1" {
		t.Skip("set RUN_POSTGRES_TESTS=1 with an isolated PostgreSQL test database")
	}
	cfg := config.Load()
	if cfg.Database.Name != "snap_flow_test" {
		t.Fatal("PostgreSQL tests require the dedicated snap_flow_test database")
	}
	if err := Connect(cfg); err != nil {
		t.Fatal(err)
	}
	admin := DB
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer adminSQL.Close()
	schema := "snapflow_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, err := url.Parse(postgresDSN(cfg.Database))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	isolated, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := isolated.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	DB = isolated
	if err := DB.AutoMigrate(&models.User{}, &models.UserImage{}, &models.UploadOperation{}, &models.OutboxEvent{}, &invite.Code{}, &limits.UserLimit{}, &confirmation.Code{}, &maintenance.Notice{}, &maintenance.Deletion{}); err != nil {
		t.Fatal(err)
	}
	if err := DB.Exec("ALTER TABLE upload_operations DROP COLUMN IF EXISTS failure_code, DROP COLUMN IF EXISTS failed_at").Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Exec("CREATE TABLE schema_migrations (id varchar(128) PRIMARY KEY, applied_at timestamptz)").Error; err != nil {
		t.Fatal(err)
	}
	legacy := models.User{Name: "Existing", Email: "existing@example.test", Password: "hash"}
	if err := DB.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Exec("INSERT INTO upload_operations (filename, user_id, state, size_bytes) VALUES (?, ?, ?, ?)", "preserved.jpg", legacy.ID, "complete", 123).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Exec("INSERT INTO upload_operations (filename, user_id, state, size_bytes) VALUES (?, ?, ?, ?)", "old-deletion.jpg", legacy.ID, "deleted", 50).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := runMigrations(); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	var preserved models.UploadOperation
	if err := DB.First(&preserved, "filename = ?", "preserved.jpg").Error; err != nil || preserved.SizeBytes != 123 {
		t.Fatal("upload operation not retained", err)
	}
	var retry maintenance.Deletion
	if err := DB.First(&retry, "filename = ?", "old-deletion.jpg").Error; err != nil || retry.CompletedAt != nil {
		t.Fatal("legacy deletion not scheduled", err)
	}
	var retained models.User
	if err := DB.First(&retained, legacy.ID).Error; err != nil || retained.Email != legacy.Email {
		t.Fatal("legacy data not retained", err)
	}
	for _, model := range []any{&models.User{}, &models.UserImage{}} {
		if !DB.Migrator().HasTable(model) {
			t.Fatal("missing model table")
		}
	}
	if !DB.Migrator().HasIndex(&models.UserImage{}, "idx_user_images_owner_image") {
		t.Fatal("missing query indexes")
	}
	user := models.User{Name: "Owner", Email: "owner@example.test", Password: "hash"}
	if err := DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&models.User{Name: "Duplicate", Email: user.Email, Password: "hash"}).Error; err == nil {
		t.Fatal("duplicate email accepted")
	}
	badID := user.ID + 99999
	if err := DB.Create(&models.UserImage{UserID: badID, Image: "bad.jpg"}).Error; err == nil {
		t.Fatal("invalid image owner foreign key accepted")
	}
	image := models.UserImage{UserID: user.ID, Image: "a.jpg"}
	if err := DB.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := DB.Model(&models.UserImage{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("image count failed: %v", err)
	}
	var owned models.UserImage
	if err := DB.Where("image = ? AND user_id = ?", "a.jpg", user.ID).First(&owned).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Where("image = ? AND user_id = ?", "a.jpg", badID).First(&models.UserImage{}).Error; err != gorm.ErrRecordNotFound {
		t.Fatal("image was found for a different owner")
	}
	if err := DB.Delete(&owned).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.First(&models.UserImage{}, image.ID).Error; err != gorm.ErrRecordNotFound {
		t.Fatal("soft-deleted image is still visible")
	}
	var deleted models.UserImage
	if err := DB.Unscoped().First(&deleted, image.ID).Error; err != nil || !deleted.DeletedAt.Valid {
		t.Fatalf("soft-delete marker missing: %v", err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return tx.Create(&models.UserImage{UserID: user.ID, Image: "batch.jpg"}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := DB.Where("user_id = ?", user.ID).Delete(&models.UserImage{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&models.UserImage{}).Where("user_id = ?", user.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("batch delete failed: %v", err)
	}
}
