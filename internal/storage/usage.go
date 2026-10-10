package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/service/s3"
)

// Usage measures stored ciphertext, not plaintext image sizes or allocated disk blocks.
type Usage struct {
	Originals  int64
	Thumbnails int64
	Staging    int64
}

// ImageUsage is intended for periodic monitoring, never for request processing.
func (s *EncryptedStorage) ImageUsage(ctx context.Context) (Usage, error) {
	switch objects := s.objects.(type) {
	case *localObjects:
		return objects.imageUsage(ctx)
	case *s3Objects:
		return objects.imageUsage(ctx)
	default:
		return Usage{}, errors.New("storage usage is not supported")
	}
}

func (s *localObjects) imageUsage(ctx context.Context) (Usage, error) {
	var result Usage
	if err := checkDirectory(s.root); err != nil {
		return result, err
	}
	private := filepath.Join(s.root, "private")
	if err := checkDirectory(private); err != nil {
		return result, err
	}
	for _, item := range []struct {
		name  string
		total *int64
	}{
		{"uploads", &result.Originals}, {"thumbnails", &result.Thumbnails}, {"staging", &result.Staging},
	} {
		path := filepath.Join(private, item.name)
		if err := checkDirectory(path); err != nil {
			return Usage{}, err
		}
		dir, err := os.Open(path) // #nosec G304 -- path is a fixed subdirectory of the validated storage root.
		if err != nil {
			return Usage{}, err
		}
		err = countRegularFiles(ctx, dir, item.total)
		if closeErr := dir.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return Usage{}, err
		}
	}
	return result, nil
}

func countRegularFiles(ctx context.Context, dir *os.File, total *int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(512)
		for _, entry := range entries {
			info, statErr := entry.Info()
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			} // A concurrent rename or delete.
			if statErr != nil {
				return statErr
			}
			if info.Mode().IsRegular() {
				if info.Size() < 0 || info.Size() > math.MaxInt64-*total {
					return errors.New("image storage size overflow")
				}
				*total += info.Size()
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

type s3UsageLister interface {
	ListObjectsV2PagesWithContext(aws.Context, *s3.ListObjectsV2Input, func(*s3.ListObjectsV2Output, bool) bool, ...request.Option) error
}

func (s *s3Objects) imageUsage(ctx context.Context) (Usage, error) {
	client, ok := s.client.(s3UsageLister)
	if !ok {
		return Usage{}, errors.New("S3 usage listing is not supported")
	}
	var result Usage
	for _, item := range []struct {
		prefix string
		total  *int64
	}{
		{"private/uploads/", &result.Originals}, {"private/thumbnails/", &result.Thumbnails}, {"private/staging/", &result.Staging},
	} {
		input := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(item.prefix)}
		var pageErr error
		if err := client.ListObjectsV2PagesWithContext(ctx, input, func(page *s3.ListObjectsV2Output, _ bool) bool {
			if page == nil {
				pageErr = errors.New("empty S3 listing page")
				return false
			}
			for _, object := range page.Contents {
				if object == nil || object.Size == nil || *object.Size < 0 || *object.Size > math.MaxInt64-*item.total {
					pageErr = errors.New("invalid S3 object size")
					return false
				}
				*item.total += *object.Size
			}
			return ctx.Err() == nil
		}); err != nil {
			return Usage{}, fmt.Errorf("list image objects: %w", err)
		}
		if pageErr != nil {
			return Usage{}, pageErr
		}
		if err := ctx.Err(); err != nil {
			return Usage{}, err
		}
	}
	return result, nil
}
