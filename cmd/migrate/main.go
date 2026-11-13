package main

import (
	"context"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/logging"

	"github.com/joho/godotenv"
)

func main() {
	logging.Init()
	_ = godotenv.Load()
	cfg := config.Load()
	if err := database.Connect(cfg); err != nil {
		logging.Fatal()
	}
	if err := database.Migrate(); err != nil {
		logging.Fatal()
	}
	if err := database.EnsureAdmin(database.GetDB(), cfg.Admin); err != nil {
		logging.Event(context.Background(), "ERROR", "admin_migration_failed", "error_code", "invalid_admin_configuration_or_conflict")
		logging.Fatal()
	}
}
