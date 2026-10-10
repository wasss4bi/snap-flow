package observability

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPDuration       = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "snapflow_http_request_duration_seconds", Help: "HTTP request duration by route, method and status.", Buckets: prometheus.DefBuckets}, []string{"method", "route", "status"})
	SQLDuration        = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "snapflow_sql_query_duration_seconds", Help: "GORM SQL duration grouped by operation and normalized query fingerprint.", Buckets: prometheus.DefBuckets}, []string{"query_id", "operation"})
	sqlCatalog         = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "snapflow_sql_query_info", Help: "Bounded catalog of SQL operations and parameter-free templates."}, []string{"query_id", "operation", "sql_template"})
	Errors             = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "snapflow_errors_total", Help: "Application error events by severity."}, []string{"severity"})
	OldestFailedUpload = prometheus.NewGauge(prometheus.GaugeOpts{Name: "snapflow_oldest_failed_upload_seconds", Help: "Age in seconds of the oldest upload awaiting recovery after a technical failure."})
	httpExtremes       = newExtremes("snapflow_http_request", []string{"method", "route", "status"})
	sqlExtremes        = newExtremes("snapflow_sql_query", []string{"query_id", "operation"})
	sqlIDs             = struct {
		sync.Mutex
		values map[string]struct{}
	}{values: map[string]struct{}{}}
)

func init() {
	prometheus.MustRegister(HTTPDuration, SQLDuration, Errors, OldestFailedUpload, httpExtremes, sqlExtremes, sqlCatalog)
}
func Handler() http.Handler { return promhttp.Handler() }
func ObserveHTTP(method, route, status string, d time.Duration) {
	if route == "" {
		route = "<unmatched>"
	}
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		method = "OTHER"
	}
	s := d.Seconds()
	HTTPDuration.WithLabelValues(method, route, status).Observe(s)
	httpExtremes.observe(s, method, route, status)
}
func ObserveSQL(query, operation string, d time.Duration) {
	id := SQLFingerprint(query)
	key := id + "\x00" + operation
	sqlIDs.Lock()
	if _, ok := sqlIDs.values[key]; !ok {
		if len(sqlIDs.values) >= 300 {
			id = "other"
			operation = "other"
		} else {
			sqlIDs.values[key] = struct{}{}
			sqlCatalog.WithLabelValues(id, operation, SQLTemplate(query)).Set(1)
		}
	}
	sqlIDs.Unlock()
	s := d.Seconds()
	SQLDuration.WithLabelValues(id, operation).Observe(s)
	sqlExtremes.observe(s, id, operation)
}

// SQLTemplate fails closed for statements containing string literals, comments,
// or dollar-quoted strings. It never renders GORM's parameter values.
func SQLTemplate(query string) string {
	if len(query) > 16384 || strings.ContainsAny(query, "'`\\") || strings.Contains(query, "--") || strings.Contains(query, "/*") || dollarQuote.MatchString(query) {
		return "[template unavailable: contains literal or comment]"
	}
	return strings.TrimSpace(whitespace.ReplaceAllString(sqlNumbers.ReplaceAllStringFunc(query, func(s string) string {
		if strings.HasPrefix(s, "$") {
			return s
		}
		return "?"
	}), " "))
}

var dollarQuote = regexp.MustCompile(`\$[a-zA-Z_][a-zA-Z_0-9]*\$|\$\$`)
var sqlNumbers = regexp.MustCompile(`\$[0-9]+|\b[0-9]+(?:\.[0-9]+)?\b`)

// SQLFingerprint is derived from a redacted statement.
// GORM's trace SQL can contain interpolated values and must not be used here.
var sqlLiterals = regexp.MustCompile(`(?s)'(?:''|[^'])*'|\$[a-zA-Z_][a-zA-Z_0-9]*\$.*?\$[a-zA-Z_][a-zA-Z_0-9]*\$|\$\$.*?\$\$|--[^\n]*|/\*.*?\*/|\b[0-9]+(?:\.[0-9]+)?\b`)
var whitespace = regexp.MustCompile(`\s+`)

func SQLFingerprint(query string) string {
	// Limit work and label cardinality for malformed or unusually large SQL.
	if len(query) > 16384 {
		return "oversize"
	}
	normalized := strings.ToLower(strings.TrimSpace(whitespace.ReplaceAllString(sqlLiterals.ReplaceAllString(query, "?"), " ")))
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", sum[:8])
}

type observation struct {
	labels   []string
	min, max float64
}
type extremes struct {
	mu       sync.Mutex
	values   map[string]observation
	min, max *prometheus.Desc
}

func newExtremes(name string, labels []string) *extremes {
	return &extremes{values: map[string]observation{}, min: prometheus.NewDesc(name+"_interval_min_seconds", "Minimum observed during the last scrape interval.", labels, nil), max: prometheus.NewDesc(name+"_interval_max_seconds", "Maximum observed during the last scrape interval.", labels, nil)}
}
func (e *extremes) observe(value float64, labels ...string) {
	key := strings.Join(labels, "\x00")
	e.mu.Lock()
	defer e.mu.Unlock()
	v, ok := e.values[key]
	if !ok {
		if len(e.values) >= 1000 {
			return
		}
		v = observation{labels: append([]string(nil), labels...), min: value, max: value}
	}
	if value < v.min {
		v.min = value
	}
	if value > v.max {
		v.max = value
	}
	e.values[key] = v
}
func (e *extremes) Describe(ch chan<- *prometheus.Desc) { ch <- e.min; ch <- e.max }
func (e *extremes) Collect(ch chan<- prometheus.Metric) {
	e.mu.Lock()
	values := e.values
	e.values = map[string]observation{}
	e.mu.Unlock()
	for _, v := range values {
		ch <- prometheus.MustNewConstMetric(e.min, prometheus.GaugeValue, v.min, v.labels...)
		ch <- prometheus.MustNewConstMetric(e.max, prometheus.GaugeValue, v.max, v.labels...)
	}
}
