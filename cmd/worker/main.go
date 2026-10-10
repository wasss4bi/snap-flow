package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"snap-flow-go/internal/logging"
	"syscall"

	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/queue"

	"github.com/joho/godotenv"
)

func main() {
	logging.Init()
	_ = godotenv.Load()
	cfg := config.Load()
	if err := database.Connect(cfg); err != nil {
		logging.Fatal()
	}
	store, err := storage.New(cfg)
	if err != nil {
		logging.Fatal()
	}
	publisher, err := queue.NewRabbitPublisher(cfg.RabbitMQ)
	if err != nil {
		logging.Fatal()
	}
	defer func() {
		if err := publisher.Close(); err != nil {
			logging.Event(context.Background(), "WARNING", "queue_close_failed", "error_code", "cleanup_failed")
		}
	}()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	log.Println("Starting RabbitMQ worker")
	go queue.RunParkingRecovery(ctx, publisher)
	if err := queue.RunWorker(ctx, publisher, queue.HandleWithFailureTracking(queue.NewTaskHandler(store).Handle, publisher.MaxAttempts())); err != nil {
		logging.Fatal()
	}
}
