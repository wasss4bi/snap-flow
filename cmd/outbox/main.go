package main

import (
	"context"
	"os"
	"os/signal"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/logging"
	mailservice "snap-flow-go/internal/mail"
	"snap-flow-go/internal/mailqueue"
	"snap-flow-go/internal/maintenance"
	"snap-flow-go/internal/storage"
	"snap-flow-go/pkg/queue"
	"strings"
	"syscall"
)

func main() {
	logging.Init()
	cfg := config.Load()
	if strings.TrimSpace(cfg.Auth.JWTSecret) == "" {
		logging.Fatal()
	}
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
	mailer := &mailservice.SMTP{Host: cfg.Mail.Host, Port: cfg.Mail.Port, User: cfg.Mail.User, Password: cfg.Mail.Password, From: cfg.Mail.From, RequireTLS: cfg.Mail.RequireTLS}
	go maintenance.Run(ctx, database.GetDB(), store, mailer)
	go mailqueue.Run(ctx, database.GetDB(), mailer, cfg.Auth.JWTSecret)
	queue.RunOutbox(ctx, publisher, store.(storage.StagingStorage))
}
