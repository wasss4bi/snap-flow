package database

import (
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"snap-flow-go/internal/observability"
)

func TestSQLMetricsDoNotExportParameters(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := registerMetrics(db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.Raw("SELECT ?", 37).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT ?", 42).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	var measured dto.Metric
	if err := observability.SQLDuration.WithLabelValues(observability.SQLFingerprint("SELECT ?"), "unknown").(interface{ Write(*dto.Metric) error }).Write(&measured); err != nil {
		t.Fatal(err)
	}
	if measured.GetHistogram().GetSampleCount() != 2 {
		t.Fatalf("expected both executions under one SQL fingerprint, got %d", measured.GetHistogram().GetSampleCount())
	}
	response := httptest.NewRecorder()
	observability.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, "snapflow_sql_query_duration_seconds_count") {
		t.Fatalf("SQL metrics absent")
	}
	if !strings.Contains(body, "SELECT ?") || strings.Contains(body, "SELECT 37") || strings.Contains(body, "SELECT 42") {
		t.Fatalf("SQL template missing or parameters exposed")
	}
}
