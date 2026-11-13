package logging

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm/logger"
	"log"
	"log/slog"
	"os"
	"regexp"
	"time"
)

type requestKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestKey{}, id)
}
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestKey{}).(string); return id }
func Init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.LevelKey {
			level := a.Value.Any().(slog.Level)
			name := "INFO"
			if level >= slog.LevelError+4 {
				name = "FATAL"
			} else if level >= slog.LevelError {
				name = "ERROR"
			} else if level >= slog.LevelWarn {
				name = "WARNING"
			}
			return slog.String("level", name)
		}
		return a
	}})))
	log.SetFlags(0)
	log.SetOutput(logWriter{})
	gin.DefaultWriter = logWriter{}
	gin.DefaultErrorWriter = logWriter{}
}

type logWriter struct{}

func (logWriter) Write(data []byte) (int, error) {
	slog.Info("component_message", "message", string(data))
	return len(data), nil
}
func Event(ctx context.Context, level, event string, fields ...any) {
	l := slog.LevelInfo
	switch level {
	case "WARNING":
		l = slog.LevelWarn
	case "ERROR":
		l = slog.LevelError
	case "FATAL":
		l = slog.LevelError + 4
	}
	args := []any{"request_id", RequestID(ctx)}
	args = append(args, fields...)
	slog.Log(ctx, l, event, args...)
}
func Fatal() {
	Event(context.Background(), "FATAL", "startup_failed", "error_code", "configuration_or_dependency_failed")
	os.Exit(1)
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func HTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-ID")
		if !validID.MatchString(id) {
			id = uuid.NewString()
		}
		c.Header("X-Request-ID", id)
		c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), id))
		start := time.Now()
		defer func() {
			if recover() != nil {
				Event(c.Request.Context(), "ERROR", "http_panic", "error_code", "handler_panic")
				c.AbortWithStatus(500)
			}
			Event(c.Request.Context(), "INFO", "http_request", "method", c.Request.Method, "route", c.FullPath(), "status", c.Writer.Status(), "duration_ms", time.Since(start).Milliseconds())
		}()
		c.Next()
	}
}

type GORM struct{}

func (GORM) LogMode(logger.LogLevel) logger.Interface { return GORM{} }
func (GORM) Info(ctx context.Context, _ string, _ ...interface{}) {
	Event(ctx, "INFO", "database_info")
}
func (GORM) Warn(ctx context.Context, _ string, _ ...interface{}) {
	Event(ctx, "WARNING", "database_warning")
}
func (GORM) Error(ctx context.Context, _ string, _ ...interface{}) {
	Event(ctx, "ERROR", "database_error", "error_code", "database_operation_failed")
}
func (GORM) Trace(ctx context.Context, _ time.Time, _ func() (string, int64), err error) {
	if err != nil && err != logger.ErrRecordNotFound {
		Event(ctx, "ERROR", "database_query_failed", "error_code", "query_failed")
	}
}
