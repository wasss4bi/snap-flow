package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
)

func TestLocalImageUsageCountsCiphertextAndStaging(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStorage(root, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"uploads/a.jpg": 23, "thumbnails/a.jpg": 11, "staging/.image-temp": 7} {
		if err := os.WriteFile(filepath.Join(root, "private", filepath.FromSlash(name)), make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, make([]byte, 1000), 0600); err != nil {
		t.Fatal(err)
	}
	// Symlink creation may require privileges on Windows.
	_ = os.Symlink(outside, filepath.Join(root, "private", "uploads", "link"))
	got, err := store.ImageUsage(context.Background())
	if err != nil || got != (Usage{Originals: 23, Thumbnails: 11, Staging: 7}) {
		t.Fatalf("usage=%+v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ImageUsage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, "private", "staging")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImageUsage(context.Background()); err == nil {
		t.Fatal("missing directory did not fail scan")
	}
}

type usageS3 struct {
	memoryS3
	failure error
	calls   int
	invalid bool
}

func (m *usageS3) ListObjectsV2PagesWithContext(ctx aws.Context, input *s3.ListObjectsV2Input, callback func(*s3.ListObjectsV2Output, bool) bool, _ ...request.Option) error {
	if m.failure != nil {
		return m.failure
	}
	if aws.StringValue(input.Bucket) != "test" {
		return errors.New("incorrect bucket")
	}
	if m.invalid {
		callback(&s3.ListObjectsV2Output{Contents: []*s3.Object{{}}}, true)
		return nil
	}
	m.calls++
	var sizes []int64
	switch aws.StringValue(input.Prefix) {
	case "private/uploads/":
		sizes = []int64{7, 13}
	case "private/thumbnails/":
		sizes = []int64{3, 7}
	case "private/staging/":
		sizes = []int64{15, 25}
	default:
		return errors.New("unexpected prefix")
	}
	for i, size := range sizes {
		if !callback(&s3.ListObjectsV2Output{Contents: []*s3.Object{{Size: aws.Int64(size)}}}, i == len(sizes)-1) {
			break
		}
	}
	return ctx.Err()
}
func TestS3ImageUsageCountsEveryPageAndReportsErrors(t *testing.T) {
	client := &usageS3{}
	store := newEncryptedStorage(&s3Objects{client: client, bucket: "test"}, "test-key")
	got, err := store.ImageUsage(context.Background())
	if err != nil || got != (Usage{Originals: 20, Thumbnails: 10, Staging: 40}) || client.calls != 3 {
		t.Fatalf("usage=%+v calls=%d err=%v", got, client.calls, err)
	}
	client.failure = errors.New("list denied")
	if _, err := store.ImageUsage(context.Background()); err == nil {
		t.Fatal("S3 listing failure ignored")
	}
	client.failure = nil
	client.invalid = true
	if _, err := store.ImageUsage(context.Background()); err == nil {
		t.Fatal("invalid S3 object size ignored")
	}
}
