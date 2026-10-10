package database

import (
	"fmt"
	"log"
	"net"
	"net/url"

	"snap-flow-go/internal/logging"

	"snap-flow-go/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var DB *gorm.DB

func postgresDSN(cfg config.DatabaseConfig) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, cfg.Port),
		Path:   "/" + cfg.Name,
	}
	query := u.Query()
	query.Set("sslmode", cfg.SSLMode)
	query.Set("timezone", "UTC")
	u.RawQuery = query.Encode()
	return u.String()
}

func Connect(cfg *config.Config) error {
	var err error
	DB, err = gorm.Open(postgres.Open(postgresDSN(cfg.Database)), &gorm.Config{
		Logger: logging.GORM{},
	})
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}
	if err := registerMetrics(DB); err != nil {
		return fmt.Errorf("register database metrics: %w", err)
	}
	log.Println("Database connected")
	return nil
}

// Migrate is run by cmd/migrate before either application process starts.
func Migrate() error {
	if err := runMigrations(); err != nil {
		return err
	}
	log.Println("Database migration completed")
	return nil
}

func GetDB() *gorm.DB { return DB }
