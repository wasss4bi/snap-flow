package database

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	gormpostgres "gorm.io/driver/postgres"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func runMigrations() (resultErr error) {
	dialect, ok := DB.Dialector.(*gormpostgres.Dialector)
	if !ok {
		return errors.New("versioned migrations require PostgreSQL")
	}
	conn, err := sql.Open("pgx", dialect.DSN)
	if err != nil {
		return fmt.Errorf("open migration connection: %w", err)
	}
	// The older GORM-based release used schema_migrations with a different shape.
	driver, err := migratepostgres.WithInstance(conn, &migratepostgres.Config{MigrationsTable: "snapflow_schema_migrations"})
	if err != nil {
		return errors.Join(fmt.Errorf("create migration driver: %w", err), conn.Close())
	}
	src, err := iofs.New(migrationFiles, "migrations")
	if err != nil {
		return errors.Join(err, driver.Close())
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return errors.Join(err, src.Close(), driver.Close())
	}
	defer func() {
		sourceErr, databaseErr := m.Close()
		resultErr = errors.Join(resultErr, sourceErr, databaseErr)
	}()
	if err = m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
