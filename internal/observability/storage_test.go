package observability

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"snap-flow-go/internal/storage"
)

type testUsageReader struct {
	usage storage.Usage
	err   error
}

func (r testUsageReader) ImageUsage(context.Context) (storage.Usage, error) { return r.usage, r.err }

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var result dto.Metric
	if err := g.Write(&result); err != nil {
		t.Fatal(err)
	}
	return result.GetGauge().GetValue()
}

func TestImageStorageMetricsReportFailureWithoutReplacingLastValues(t *testing.T) {
	scanImageStorage(context.Background(), testUsageReader{usage: storage.Usage{Originals: 12, Thumbnails: 3, Staging: 2}})
	if got := gaugeValue(t, imageBytes.WithLabelValues("originals")); got != 12 {
		t.Fatalf("originals=%v", got)
	}
	if got := gaugeValue(t, imageScanOK.WithLabelValues()); got != 1 {
		t.Fatalf("scan status=%v", got)
	}
	scanImageStorage(context.Background(), testUsageReader{err: errors.New("scan failed")})
	if got := gaugeValue(t, imageScanOK.WithLabelValues()); got != 0 {
		t.Fatalf("failed scan status=%v", got)
	}
	if got := gaugeValue(t, imageBytes.WithLabelValues("originals")); got != 12 {
		t.Fatalf("last size changed on failure: %v", got)
	}
}
