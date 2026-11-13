package main

import (
	"context"
	"github.com/redis/go-redis/v9"
	"log"
	"net/http"
	"os"
	"os/signal"
	"snap-flow-go/internal/api"
	"snap-flow-go/internal/auth"
	"snap-flow-go/internal/config"
	"snap-flow-go/internal/database"
	"snap-flow-go/internal/logging"
	mailservice "snap-flow-go/internal/mail"
	"snap-flow-go/internal/ratelimit"
	"snap-flow-go/internal/storage"
	"snap-flow-go/internal/swagger"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"snap-flow-go/pkg/queue"
)

func main() {
	logging.Init()
	gin.SetMode(gin.ReleaseMode)
	// Загружаем переменные окружения из .env файла (если существует)
	godotenv.Load()

	// Загружаем конфигурацию
	cfg := config.Load()

	if strings.TrimSpace(cfg.Auth.JWTSecret) == "" {
		log.Print("JWT_SECRET is required")
		os.Exit(1)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.Auth.RedisAddr})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Print("Redis unavailable")
		os.Exit(1)
	}
	defer redisClient.Close()
	// Подключаемся к базе данных
	if err := database.Connect(cfg); err != nil {
		logging.Fatal()
	}

	// Выбираем хранилище по конфигурации
	imageStorage, err := storage.New(cfg)
	if err != nil {
		logging.Fatal()
	}

	// Инициализируем JWT менеджер
	jwtManager := auth.NewJWTManager(cfg.Auth.JWTSecret)

	// Инициализируем клиент очереди
	queueClient, err := queue.NewRabbitPublisher(cfg.RabbitMQ)
	if err != nil {
		logging.Fatal()
	}
	defer queueClient.Close()

	// Инициализируем обработчики API
	mailer := &mailservice.SMTP{Host: cfg.Mail.Host, Port: cfg.Mail.Port, User: cfg.Mail.User, Password: cfg.Mail.Password, From: cfg.Mail.From, BaseURL: cfg.Mail.BaseURL, RequireTLS: cfg.Mail.RequireTLS}
	authHandler := api.NewAuthHandler(jwtManager, mailer)
	authHandler.SetLoginLimiter(ratelimit.New(redisClient))
	authHandler.SetMailSecret(cfg.Auth.JWTSecret)
	imageHandler := api.NewImageHandler(imageStorage, queueClient)

	// Настраиваем Gin
	if gin.Mode() == gin.ReleaseMode {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	var trusted []string
	if cfg.Auth.TrustedProxies != "" {
		for _, item := range strings.Split(cfg.Auth.TrustedProxies, ",") {
			trusted = append(trusted, strings.TrimSpace(item))
		}
	}
	if err := router.SetTrustedProxies(trusted); err != nil {
		log.Print("Invalid TRUSTED_PROXIES")
		os.Exit(1)
	}
	router.Use(logging.HTTP())

	// Middleware для CORS
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// API маршруты без аутентификации
	apiRoutes := router.Group("/api")
	{
		apiRoutes.POST("/register", authHandler.Register)
		apiRoutes.POST("/login", authHandler.Login)
		apiRoutes.POST("/verify-email", authHandler.VerifyEmail)
		apiRoutes.POST("/resend-verification", authHandler.ResendVerification)
	}

	swagger.RegisterRoutes(router, jwtManager, "api/openapi.yml")

	// API маршруты с токен-аутентификацией
	apiAuth := router.Group("/api")
	apiAuth.Use(auth.JWTMiddleware(jwtManager))
	{
		apiAuth.POST("/upload-images", imageHandler.UploadImages)
		apiAuth.GET("/get-images-count", imageHandler.GetImageCount)
		apiAuth.GET("/images", imageHandler.ListImages)
		apiAuth.GET("/uploads/:filename", imageHandler.UploadStatus)
		apiAuth.DELETE("/delete-image/:id", imageHandler.DeleteImage)
		apiAuth.DELETE("/delete-images", imageHandler.DeleteAllImages)
	}

	// Веб-маршруты без аутентификации
	router.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Snap Flow API Server",
			"version": "1.0.0",
			"status":  "running",
		})
	})

	router.GET("/auth", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Authentication page",
		})
	})

	router.GET("/politics", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "Privacy policy page",
		})
	})

	// Веб-маршруты с JWT аутентификацией
	webAuth := router.Group("")
	webAuth.Use(auth.JWTMiddleware(jwtManager))
	{
		webAuth.GET("/home", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"message": "Home page",
			})
		})

		webAuth.GET("/profile", authHandler.GetProfile)
		webAuth.POST("/logout", authHandler.Logout)
		webAuth.GET("/images/:filename", imageHandler.ViewImage)
		webAuth.GET("/thumbnails/:filename", imageHandler.ViewThumbnail)
		webAuth.DELETE("/delete-image/:id", imageHandler.DeleteImage)
		webAuth.DELETE("/delete-images", imageHandler.DeleteAllImages)
	}

	// Административные маршруты
	adminHandler := api.AdminHandler{}
	admin := router.Group("/admin")
	admin.Use(auth.JWTMiddleware(jwtManager))
	admin.Use(auth.AdminMiddleware())
	{
		admin.GET("/users", adminHandler.ListUsers)
		admin.POST("/invites", adminHandler.CreateInvite)
		admin.PUT("/users/:id/limits", adminHandler.UpdateLimits)
	}

	// Запускаем сервер
	addr := cfg.Server.Host + ":" + cfg.Server.Port
	log.Printf("Starting server on %s", addr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := &http.Server{Addr: addr, Handler: router, ReadHeaderTimeout: 5 * time.Second}
	stopped := make(chan error, 1)
	go func() { stopped <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Print(err)
		}
	case err := <-stopped:
		if err != nil && err != http.ErrServerClosed {
			log.Print("Failed to start server:", err)
		}
	}
}
