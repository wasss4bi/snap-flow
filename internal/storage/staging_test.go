package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"snap-flow-go/pkg/image"
	"testing"
)

func TestStagingEncryptedAndRetrySafe(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStorage(root, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("a"), image.MaxImageSize)
	if err := store.Stage(plain, "stage.jpg"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "private", "staging", "stage.jpg"))
	if err != nil || bytes.Equal(raw, plain) {
		t.Fatal("stage not encrypted")
	}
	for i := 0; i < 2; i++ {
		got, err := store.ReadStaged("stage.jpg")
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatal("stage unavailable to replay")
		}
	}
	if err := store.Stage(append(plain, 0), "too-big.jpg"); err == nil {
		t.Fatal("oversized stage accepted")
	}
	if err := store.Stage(plain, "../escape"); err == nil {
		t.Fatal("unsafe key accepted")
	}
	for i := 0; i < 2; i++ {
		if err := store.DeleteStaged("stage.jpg"); err != nil {
			t.Fatal(err)
		}
	}
}
func TestStagingS3FailurePreservesError(t *testing.T) {
	want := errors.New("test backend failure")
	client := &memoryS3{objects: map[string][]byte{}, bucket: "test", failKey: "private/staging/a.jpg", failure: want}
	store := newEncryptedStorage(&s3Objects{client: client, bucket: "test"}, testSecret)
	if err := store.Stage([]byte("data"), "a.jpg"); !errors.Is(err, want) {
		t.Fatal("stage error swallowed")
	}
	if _, err := store.ReadStaged("a.jpg"); !errors.Is(err, want) {
		t.Fatal("read error swallowed")
	}
	if err := store.DeleteStaged("a.jpg"); !errors.Is(err, want) {
		t.Fatal("cleanup error swallowed")
	}
}
