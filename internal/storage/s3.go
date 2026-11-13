package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"snap-flow-go/internal/config"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

type s3Client interface {
	PutObject(*s3.PutObjectInput) (*s3.PutObjectOutput, error)
	GetObject(*s3.GetObjectInput) (*s3.GetObjectOutput, error)
	DeleteObject(*s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error)
}

type s3Objects struct {
	client s3Client
	bucket string
}

func NewS3Storage(cfg *config.Config) (*EncryptedStorage, error) {
	timeoutSeconds := cfg.Storage.RequestTimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds < 1 || timeoutSeconds > 3600 {
		return nil, fmt.Errorf("S3 request timeout must be between 1 and 3600 seconds")
	}
	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String(cfg.Storage.Region),
		Endpoint:         aws.String(cfg.Storage.Endpoint),
		S3ForcePathStyle: aws.Bool(true),
		HTTPClient:       &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		Credentials: credentials.NewStaticCredentials(
			cfg.Storage.AccessKey, cfg.Storage.SecretKey, "",
		),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 session: %w", err)
	}
	// Keep SHA-256(secret key) and nonce-prefixed AES-GCM for existing objects.
	return newEncryptedStorage(&s3Objects{
		client: s3.New(sess), bucket: cfg.Storage.Bucket,
	}, cfg.Storage.SecretKey), nil
}

func (s *s3Objects) put(key string, data []byte) error {
	_, err := s.client.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
	})
	return err
}

func (s *s3Objects) putContext(ctx context.Context, key string, data []byte) error {
	input := &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data)}
	if client, ok := s.client.(interface {
		PutObjectWithContext(aws.Context, *s3.PutObjectInput, ...request.Option) (*s3.PutObjectOutput, error)
	}); ok {
		_, err := client.PutObjectWithContext(ctx, input)
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.PutObject(input)
	return err
}

func (s *s3Objects) get(key string) ([]byte, error) {
	object, err := s.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	return io.ReadAll(object.Body)
}

func (s *s3Objects) delete(key string) error {
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	return err
}
