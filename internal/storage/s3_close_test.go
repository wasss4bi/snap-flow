package storage

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/service/s3"
)

type failingCloseBody struct {
	io.Reader
	err error
}

func (b failingCloseBody) Close() error { return b.err }

type closeErrorS3 struct {
	memoryS3
	body io.ReadCloser
}

func (s *closeErrorS3) GetObject(*s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	return &s3.GetObjectOutput{Body: s.body}, nil
}

func TestS3GetReportsCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	client := &closeErrorS3{body: failingCloseBody{Reader: strings.NewReader("data"), err: closeErr}}
	objects := &s3Objects{client: client, bucket: "test"}
	data, err := objects.get("object")
	if string(data) != "data" || !errors.Is(err, closeErr) {
		t.Fatalf("get returned data %q and error %v", data, err)
	}
}
