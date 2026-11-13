package logging

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestIDValidationAndResponse(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(HTTP())
	r.GET("/", func(c *gin.Context) {
		if RequestID(c.Request.Context()) == "" {
			t.Error("request ID missing")
		}
		c.Status(200)
	})
	for _, id := range []string{"trace-valid_123", "bad id", ""} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Request-ID", id)
		res := httptest.NewRecorder()
		r.ServeHTTP(res, req)
		got := res.Header().Get("X-Request-ID")
		if !validID.MatchString(got) || (id == "trace-valid_123" && got != id) {
			t.Fatal("invalid request ID")
		}
	}
}
func TestDatabaseLoggerNeverReadsSQL(t *testing.T) {
	GORM{}.Trace(context.Background(), time.Now(), func() (string, int64) { t.Fatal("SQL containing secrets was evaluated"); return "secret", 0 }, errors.New("sensitive database detail"))
}
