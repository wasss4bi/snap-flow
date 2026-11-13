package config

import (
	"os"
	"strconv"
)

type Config struct {
	Server   ServerConfig
	Database DatabaseConfig
	RabbitMQ RabbitMQConfig
	Storage  StorageConfig
	Auth     AuthConfig
	Admin    AdminConfig
	Mail     MailConfig
}

type ServerConfig struct {
	Port string
	Host string
}

type DatabaseConfig struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	SSLMode  string
}

type RabbitMQConfig struct {
	Host             string
	Port             string
	User             string
	Password         string
	VirtualHost      string
	QueuePrefix      string
	Prefetch         int
	MaxAttempts      int
	RetryDelayMS     int
	PublishTimeoutMS int
}

type StorageConfig struct {
	Driver                string
	LocalPath             string
	LocalKey              string
	AccessKey             string
	SecretKey             string
	Bucket                string
	Region                string
	Endpoint              string
	RequestTimeoutSeconds int
}

type MailConfig struct {
	Host, Port, User, Password, From, BaseURL string
	RequireTLS                                bool
}

type AdminConfig struct{ Name, Email, Password string }

type AuthConfig struct {
	JWTSecret      string
	SessionSecret  string
	RedisAddr      string
	TrustedProxies string
}

func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Port: getEnv("SERVER_PORT", "8080"),
			Host: getEnv("SERVER_HOST", "0.0.0.0"),
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			Name:     getEnv("DB_DATABASE", "snap_flow"),
			User:     getEnv("DB_USERNAME", "snap_flow"),
			Password: getEnv("DB_PASSWORD", ""),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		RabbitMQ: RabbitMQConfig{
			Host:             getEnv("RABBITMQ_HOST", "localhost"),
			Port:             getEnv("RABBITMQ_PORT", "5672"),
			User:             getEnv("RABBITMQ_USERNAME", "guest"),
			Password:         getEnv("RABBITMQ_PASSWORD", "guest"),
			VirtualHost:      getEnv("RABBITMQ_VHOST", "/"),
			QueuePrefix:      getEnv("RABBITMQ_QUEUE_PREFIX", "snapflow.v2"),
			Prefetch:         getEnvInt("RABBITMQ_PREFETCH", 4),
			MaxAttempts:      getEnvInt("RABBITMQ_MAX_ATTEMPTS", 3),
			RetryDelayMS:     getEnvInt("RABBITMQ_RETRY_DELAY_MS", 5000),
			PublishTimeoutMS: getEnvInt("RABBITMQ_PUBLISH_TIMEOUT_MS", 3000),
		},
		Storage: StorageConfig{
			Driver:                getEnv("STORAGE_DRIVER", "s3"),
			LocalPath:             getEnv("LOCAL_STORAGE_PATH", "./data/storage"),
			LocalKey:              getEnv("LOCAL_STORAGE_KEY", ""),
			AccessKey:             getEnv("TWS_ACCESS_KEY_ID", ""),
			SecretKey:             getEnv("TWS_SECRET_ACCESS_KEY", ""),
			Bucket:                getEnv("TWS_BUCKET", ""),
			Region:                getEnv("TWS_DEFAULT_REGION", "ru-1"),
			Endpoint:              getEnv("TWS_ENDPOINT", ""),
			RequestTimeoutSeconds: getEnvInt("S3_REQUEST_TIMEOUT_SECONDS", 30),
		},
		Mail:  MailConfig{Host: getEnv("SMTP_HOST", "localhost"), Port: getEnv("SMTP_PORT", "1025"), User: os.Getenv("SMTP_USER"), Password: os.Getenv("SMTP_PASSWORD"), From: getEnv("SMTP_FROM", "no-reply@example.test"), BaseURL: getEnv("PUBLIC_BASE_URL", "http://localhost:8080"), RequireTLS: getEnv("SMTP_REQUIRE_TLS", "true") == "true"},
		Admin: AdminConfig{Name: os.Getenv("ADMIN_NAME"), Email: os.Getenv("ADMIN_EMAIL"), Password: os.Getenv("ADMIN_PASSWORD")},
		Auth: AuthConfig{
			JWTSecret:      os.Getenv("JWT_SECRET"),
			RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
			TrustedProxies: os.Getenv("TRUSTED_PROXIES"),
			SessionSecret:  getEnv("SESSION_SECRET", "your-session-secret"),
		},
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}
