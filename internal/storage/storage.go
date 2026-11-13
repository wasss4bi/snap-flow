package storage

import (
	"fmt"
	"strings"

	"snap-flow-go/internal/config"
	"snap-flow-go/pkg/crypto"
)

// ImageStorage is the image boundary used by the HTTP handlers and the worker.
// Implementations store encrypted bytes and return plaintext to their callers.
type ImageStorage interface {
	StoreEncrypted(data []byte, filename string) error
	StoreThumbnail(data []byte, filename string) error
	GetDecrypted(filename string) ([]byte, error)
	GetDecryptedThumbnail(filename string) ([]byte, error)
	Delete(filename string) error
}

// objectStore only moves bytes. Encryption belongs to the common wrapper.
type objectStore interface {
	put(key string, data []byte) error
	get(key string) ([]byte, error)
	delete(key string) error
}

type EncryptedStorage struct {
	objects    objectStore
	encryptKey []byte
}

var _ ImageStorage = (*EncryptedStorage)(nil)

// New selects the physical backend at application startup. Empty Driver keeps
// the previous S3 default for callers that construct Config directly.
func New(cfg *config.Config) (ImageStorage, error) {
	switch cfg.Storage.Driver {
	case "", "s3":
		return NewS3Storage(cfg)
	case "local":
		return NewLocalStorage(cfg.Storage.LocalPath, cfg.Storage.LocalKey)
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Storage.Driver)
	}
}

func newEncryptedStorage(objects objectStore, secret string) *EncryptedStorage {
	return &EncryptedStorage{objects: objects, encryptKey: crypto.GenerateKey(secret)}
}

func imageKey(filename string, thumbnail bool) (string, error) {
	// A filename is one component, never a path (including Windows paths/ADS).
	if filename == "" || filename == "." || filename == ".." ||
		strings.ContainsAny(filename, "/\\:\x00") {
		return "", fmt.Errorf("invalid image filename")
	}
	if thumbnail {
		return "private/thumbnails/" + filename, nil
	}
	return "private/uploads/" + filename, nil
}

func (s *EncryptedStorage) store(data []byte, filename string, thumbnail bool) error {
	key, err := imageKey(filename, thumbnail)
	if err != nil {
		return err
	}
	encrypted, err := crypto.Encrypt(data, s.encryptKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt image: %w", err)
	}
	if err := s.objects.put(key, encrypted); err != nil {
		return fmt.Errorf("failed to store image: %w", err)
	}
	return nil
}

func (s *EncryptedStorage) read(filename string, thumbnail bool) ([]byte, error) {
	key, err := imageKey(filename, thumbnail)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.objects.get(key)
	if err != nil {
		return nil, fmt.Errorf("failed to read image: %w", err)
	}
	data, err := crypto.Decrypt(encrypted, s.encryptKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt image: %w", err)
	}
	return data, nil
}

func (s *EncryptedStorage) StoreEncrypted(data []byte, filename string) error {
	return s.store(data, filename, false)
}

func (s *EncryptedStorage) StoreThumbnail(data []byte, filename string) error {
	return s.store(data, filename, true)
}

func (s *EncryptedStorage) GetDecrypted(filename string) ([]byte, error) {
	return s.read(filename, false)
}

func (s *EncryptedStorage) GetDecryptedThumbnail(filename string) ([]byte, error) {
	return s.read(filename, true)
}

func (s *EncryptedStorage) Delete(filename string) error {
	for _, thumbnail := range []bool{false, true} {
		key, err := imageKey(filename, thumbnail)
		if err != nil {
			return err
		}
		if err := s.objects.delete(key); err != nil {
			return fmt.Errorf("failed to delete image: %w", err)
		}
	}
	return nil
}
