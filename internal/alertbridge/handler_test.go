package alertbridge

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerSendsCriticalAlertAndReturnsFailure(t *testing.T) {
	var got string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	h := Handler{Token: "test-token", ChatID: "123", BaseURL: tg.URL, Client: tg.Client()}
	body := `{"alerts":[{"status":"firing","labels":{"severity":"critical","alertname":"TargetDown"},"annotations":{"summary":"service unavailable"}}]}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/alerts", strings.NewReader(body)))
	if w.Code != http.StatusNoContent || !strings.Contains(got, "TargetDown") || !strings.Contains(got, "123") {
		t.Fatalf("status=%d body=%s", w.Code, got)
	}
	tg.Close()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/alerts", strings.NewReader(body)))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
