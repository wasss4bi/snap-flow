package observability

import (
	"context"
	"time"

	"snap-flow-go/internal/storage"

	"github.com/prometheus/client_golang/prometheus"
)

var imageBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "snapflow_image_storage_bytes", Help: "Bytes occupied by stored encrypted image objects, by category.",
}, []string{"category"})
var imageScanOK = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "snapflow_image_storage_scan_success", Help: "Whether the most recent image storage scan succeeded."}, nil)
var imageScanTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "snapflow_image_storage_scan_timestamp_seconds", Help: "Unix timestamp of the last successful image storage scan."}, nil)

func init() { prometheus.MustRegister(imageBytes, imageScanOK, imageScanTime) }

type imageUsageReader interface {
	ImageUsage(context.Context) (storage.Usage, error)
}

// MonitorImageStorage scans in the background; a failed scan is never shown as a valid current size.
func MonitorImageStorage(ctx context.Context, reader imageUsageReader, interval time.Duration) {
	for {
		scanCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		scanImageStorage(scanCtx, reader)
		cancel()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func scanImageStorage(ctx context.Context, reader imageUsageReader) {
	usage, err := reader.ImageUsage(ctx)
	if err != nil {
		imageScanOK.WithLabelValues().Set(0)
		return
	}
	imageBytes.WithLabelValues("originals").Set(float64(usage.Originals))
	imageBytes.WithLabelValues("thumbnails").Set(float64(usage.Thumbnails))
	imageBytes.WithLabelValues("staging").Set(float64(usage.Staging))
	imageScanTime.WithLabelValues().SetToCurrentTime()
	imageScanOK.WithLabelValues().Set(1)
}
