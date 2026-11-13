package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http/httptest"
	"testing"
	"time"
)

type memoryLimiter struct{ n int }

func (l *memoryLimiter) Begin(context.Context, string) (string, int, time.Duration, error) {
	if l.n >= 5 {
		return "", l.n, time.Minute, nil
	}
	return "lease", l.n, 0, nil
}
func (l *memoryLimiter) Finish(_ context.Context, _, _, result string) (int, error) {
	if result == "failure" {
		l.n++
	}
	if result == "success" {
		l.n = 0
	}
	return l.n, nil
}
func TestLoginReportsAndBlocksFailures(t *testing.T) {
	router, h := setupTestRouter()
	lim := &memoryLimiter{}
	h.SetLoginLimiter(lim)
	router.POST("/login", h.Login)
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest("POST", "/login", bytes.NewBufferString(`{"email":"unknown@example.test","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var body gin.H
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if i < 5 {
			if w.Code != 401 || body["attempts_remaining"] != float64(4-i) {
				t.Fatalf("attempt %d: %d %s", i, w.Code, w.Body.String())
			}
		} else if w.Code != 429 {
			t.Fatal("sixth attempt not blocked")
		}
	}
}
