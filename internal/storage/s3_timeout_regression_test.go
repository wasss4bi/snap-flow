package storage

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
	"snap-flow-go/internal/config"
)

func TestS3RequestTimeoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{{0, 30 * time.Second}, {120, 120 * time.Second}} {
		store, err := NewS3Storage(&config.Config{Storage: config.StorageConfig{Region: "test", RequestTimeoutSeconds: tc.seconds}})
		if err != nil {
			t.Fatal(err)
		}
		client := store.objects.(*s3Objects).client.(*s3.S3)
		if client.Config.HTTPClient.Timeout != tc.want {
			t.Fatalf("timeout=%v want %v", client.Config.HTTPClient.Timeout, tc.want)
		}
	}
	if _, err := NewS3Storage(&config.Config{Storage: config.StorageConfig{RequestTimeoutSeconds: 3601}}); err == nil {
		t.Fatal("out-of-range timeout accepted")
	}
}
