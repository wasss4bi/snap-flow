package storage

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"snap-flow-go/internal/config"
	"snap-flow-go/pkg/crypto"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

const testSecret = "local-test-key-not-cloud-credentials"

// Exercise the same encrypted image contract against actual files and the S3
// adapter. Only the remote SDK is replaced; AES-GCM is real in both cases.
func TestImageStorageContract(t *testing.T) {
	for _, backend := range []string{"local", "s3"} {
		t.Run(backend, func(t *testing.T) {
			var store ImageStorage
			var readRaw func(string) ([]byte, error)
			if backend == "local" {
				root := t.TempDir()
				var err error
				store, err = NewLocalStorage(root, testSecret)
				if err != nil {
					t.Fatal(err)
				}
				readRaw = func(key string) ([]byte, error) {
					return os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
				}
			} else {
				client := &memoryS3{objects: make(map[string][]byte), bucket: "test-bucket"}
				objects := &s3Objects{client: client, bucket: "test-bucket"}
				store = newEncryptedStorage(objects, testSecret)
				readRaw = objects.get
			}
			original := []byte("original bytes\x00\xff")
			thumbnail := []byte("different thumbnail bytes")
			if err := store.StoreEncrypted(original, "photo.jpg"); err != nil {
				t.Fatal(err)
			}
			if err := store.StoreThumbnail(thumbnail, "photo.jpg"); err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct {
				key   string
				plain []byte
				read  func(string) ([]byte, error)
			}{
				{"private/uploads/photo.jpg", original, store.GetDecrypted},
				{"private/thumbnails/photo.jpg", thumbnail, store.GetDecryptedThumbnail},
			} {
				raw, err := readRaw(item.key)
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(raw, item.plain) {
					t.Fatal("plaintext was stored")
				}
				// Also decode raw bytes with the old crypto primitive/key scheme.
				decoded, err := crypto.Decrypt(raw, crypto.GenerateKey(testSecret))
				if err != nil || !bytes.Equal(decoded, item.plain) {
					t.Fatalf("raw object not compatible with AES-GCM: %v", err)
				}
				got, err := item.read("photo.jpg")
				if err != nil || !bytes.Equal(got, item.plain) {
					t.Fatalf("round trip mismatch: %v", err)
				}
			}
			// Replacing an existing object must also work on Windows.
			if err := store.StoreEncrypted([]byte("replacement"), "photo.jpg"); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetDecrypted("photo.jpg")
			if err != nil || string(got) != "replacement" {
				t.Fatalf("replacement mismatch: %v", err)
			}
			for i := 0; i < 2; i++ {
				if err := store.Delete("photo.jpg"); err != nil {
					t.Fatalf("delete attempt %d: %v", i+1, err)
				}
			}
			for _, key := range []string{"private/uploads/photo.jpg", "private/thumbnails/photo.jpg"} {
				if _, err := readRaw(key); err == nil {
					t.Fatalf("object still exists: %s", key)
				}
			}
			if _, err := store.GetDecrypted("photo.jpg"); err == nil {
				t.Fatal("missing original must return an error")
			}
			if _, err := store.GetDecryptedThumbnail("photo.jpg"); err == nil {
				t.Fatal("missing thumbnail must return an error")
			}
		})
	}
}

func TestS3ReadsLegacyCiphertext(t *testing.T) {
	client := &memoryS3{objects: make(map[string][]byte), bucket: "old-bucket"}
	for _, prefix := range []string{"private/uploads/", "private/thumbnails/"} {
		// Produce the exact old format independently of the new wrapper.
		ciphertext, err := crypto.Encrypt([]byte(prefix+"legacy"), crypto.GenerateKey(testSecret))
		if err != nil {
			t.Fatal(err)
		}
		client.objects[prefix+"old.jpg"] = ciphertext
	}
	store := newEncryptedStorage(&s3Objects{client: client, bucket: "old-bucket"}, testSecret)
	for _, item := range []struct {
		read func(string) ([]byte, error)
		want string
	}{
		{store.GetDecrypted, "private/uploads/legacy"},
		{store.GetDecryptedThumbnail, "private/thumbnails/legacy"},
	} {
		got, err := item.read("old.jpg")
		if err != nil || string(got) != item.want {
			t.Fatalf("legacy ciphertext mismatch: %v", err)
		}
	}
}

func TestLocalReopenAndEncryptionErrors(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStorage(root, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreEncrypted([]byte("persistent"), "a.jpg"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLocalStorage(root, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.GetDecrypted("a.jpg")
	if err != nil || string(got) != "persistent" {
		t.Fatalf("reopen mismatch: %v", err)
	}
	wrongKey, err := NewLocalStorage(root, "another-local-test-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.GetDecrypted("a.jpg"); err == nil {
		t.Fatal("wrong key should fail")
	}
	path := filepath.Join(root, "private", "uploads", "a.jpg")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDecrypted("a.jpg"); err == nil {
		t.Fatal("tampered ciphertext should fail authentication")
	}
	if _, err := store.GetDecrypted("missing.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file should retain fs.ErrNotExist: %v", err)
	}
}

func TestLocalRejectsPaths(t *testing.T) {
	root := t.TempDir()
	store, err := NewLocalStorage(root, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"", ".", "..", "../outside.jpg", "..\\outside.jpg", "/absolute.jpg", "C:\\outside.jpg", "photo.jpg:stream", "nul\x00.jpg", "a.jpg.", "a.jpg "} {
		t.Run(filename, func(t *testing.T) {
			if err := store.StoreEncrypted([]byte("x"), filename); err == nil {
				t.Fatal("invalid original filename accepted")
			}
			if err := store.StoreThumbnail([]byte("x"), filename); err == nil {
				t.Fatal("invalid thumbnail filename accepted")
			}
			if _, err := store.GetDecrypted(filename); err == nil {
				t.Fatal("invalid read filename accepted")
			}
			if _, err := store.GetDecryptedThumbnail(filename); err == nil {
				t.Fatal("invalid thumbnail read filename accepted")
			}
			if err := store.Delete(filename); err == nil {
				t.Fatal("invalid delete filename accepted")
			}
		})
	}
	objects := store.objects.(*localObjects)
	for _, key := range []string{"../../outside.jpg", "private/uploads/../../outside.jpg", "private/other/a.jpg"} {
		if err := objects.put(key, []byte("x")); err == nil {
			t.Fatalf("invalid object key accepted: %q", key)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "private", "uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected operations left files behind: %v", err)
	}
}

func TestLocalRejectsSymlinks(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			external := filepath.Join(outside, "a.jpg")
			if err := os.WriteFile(external, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewLocalStorage(root, testSecret)
			if err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "private", "uploads", "a.jpg")
			target := external
			if kind == "directory" {
				link = filepath.Dir(link)
				target = outside
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("OS does not permit creating test symlink: %v", err)
			}
			if err := store.StoreEncrypted([]byte("changed"), "a.jpg"); err == nil {
				t.Fatal("symlink write accepted")
			}
			if _, err := store.GetDecrypted("a.jpg"); err == nil {
				t.Fatal("symlink read accepted")
			}
			if err := store.Delete("a.jpg"); err == nil {
				t.Fatal("symlink delete accepted")
			}
			got, err := os.ReadFile(external)
			if err != nil || string(got) != "outside" {
				t.Fatalf("outside file changed: %v", err)
			}
			if kind == "directory" {
				if _, err := NewLocalStorage(root, testSecret); err == nil {
					t.Fatal("constructor accepted symlink directory")
				}
			}
		})
	}
}

func TestStorageFactory(t *testing.T) {
	for _, driver := range []string{"", "s3"} {
		cfg := &config.Config{Storage: config.StorageConfig{Driver: driver, Region: "ru-1", SecretKey: testSecret}}
		store, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		encrypted := store.(*EncryptedStorage)
		if _, ok := encrypted.objects.(*s3Objects); !ok {
			t.Fatal("default backend is not S3")
		}
		if !bytes.Equal(encrypted.encryptKey, crypto.GenerateKey(testSecret)) {
			t.Fatal("S3 key derivation changed")
		}
	}
	cfg := &config.Config{Storage: config.StorageConfig{Driver: "local", LocalPath: t.TempDir(), LocalKey: testSecret}}
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Storage.LocalKey = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("empty local key accepted")
	}
	if _, err := NewLocalStorage("", testSecret); err == nil {
		t.Fatal("empty directory accepted")
	}
	cfg.Storage.Driver = "unknown"
	if _, err := New(cfg); err == nil {
		t.Fatal("unknown backend accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalStorage(file, testSecret); err == nil {
		t.Fatal("regular file accepted as root directory")
	}
}

func TestStoragePropagatesBackendErrors(t *testing.T) {
	want := errors.New("backend unavailable")
	for _, failingKey := range []string{"private/uploads/a.jpg", "private/thumbnails/a.jpg"} {
		client := &memoryS3{objects: make(map[string][]byte), bucket: "test", failKey: failingKey, failure: want}
		store := newEncryptedStorage(&s3Objects{client: client, bucket: "test"}, testSecret)
		var err error
		if failingKey == "private/uploads/a.jpg" {
			err = store.StoreEncrypted([]byte("x"), "a.jpg")
		} else {
			err = store.StoreThumbnail([]byte("x"), "a.jpg")
		}
		if !errors.Is(err, want) {
			t.Fatalf("put error lost: %v", err)
		}
		if failingKey == "private/uploads/a.jpg" {
			_, err = store.GetDecrypted("a.jpg")
		} else {
			_, err = store.GetDecryptedThumbnail("a.jpg")
		}
		if !errors.Is(err, want) {
			t.Fatalf("get error lost: %v", err)
		}
		if err := store.Delete("a.jpg"); !errors.Is(err, want) {
			t.Fatalf("delete error lost: %v", err)
		}
	}
}

// This fake checks the actual SDK adapter's bucket/key/body contract, rather
// than replacing the encrypted storage used by application callers.
type memoryS3 struct {
	objects map[string][]byte
	bucket  string
	failKey string
	failure error
}

func (m *memoryS3) check(bucket, key *string) error {
	if aws.StringValue(bucket) != m.bucket {
		return errors.New("unexpected bucket")
	}
	if aws.StringValue(key) == m.failKey {
		return m.failure
	}
	return nil
}

func (m *memoryS3) PutObject(in *s3.PutObjectInput) (*s3.PutObjectOutput, error) {
	if err := m.check(in.Bucket, in.Key); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	m.objects[aws.StringValue(in.Key)] = data
	return &s3.PutObjectOutput{}, nil
}

func (m *memoryS3) GetObject(in *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	if err := m.check(in.Bucket, in.Key); err != nil {
		return nil, err
	}
	data, ok := m.objects[aws.StringValue(in.Key)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (m *memoryS3) DeleteObject(in *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
	if err := m.check(in.Bucket, in.Key); err != nil {
		return nil, err
	}
	delete(m.objects, aws.StringValue(in.Key))
	return &s3.DeleteObjectOutput{}, nil
}
