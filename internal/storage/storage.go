package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
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
	objects          objectStore
	encryptKey       []byte
	keyVersion       string
	keys             map[string][]byte
	legacyKey        []byte
	requireLegacyKey bool
	writeLegacy      bool
}

var _ ImageStorage = (*EncryptedStorage)(nil)

// New selects the physical backend at application startup. Empty Driver keeps
// the previous S3 default for callers that construct Config directly.
func New(cfg *config.Config) (ImageStorage, error) {
	var store *EncryptedStorage
	var err error
	switch cfg.Storage.Driver {
	case "", "s3":
		store, err = NewS3Storage(cfg)
	case "local":
		store, err = NewLocalStorage(cfg.Storage.LocalPath, cfg.Storage.LocalKey)
	default:
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Storage.Driver)
	}
	if err != nil {
		return nil, err
	}
	active := cfg.Storage.EncryptionKey
	if active == "" && cfg.Storage.Driver == "local" {
		active = cfg.Storage.LocalKey
	}
	if active == "" {
		return nil, fmt.Errorf("STORAGE_ENCRYPTION_KEY is required")
	}
	version := cfg.Storage.EncryptionKeyVersion
	if version == "" {
		version = "v1"
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(version) {
		return nil, fmt.Errorf("invalid storage encryption key version")
	}
	previous := map[string]string{}
	if cfg.Storage.PreviousEncryptionKeys != "" {
		if err := json.Unmarshal([]byte(cfg.Storage.PreviousEncryptionKeys), &previous); err != nil {
			return nil, fmt.Errorf("invalid previous storage encryption keys")
		}
	}
	store.keys = make(map[string][]byte, len(previous)+1)
	for id, secret := range previous {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(id) || secret == "" || id == version {
			return nil, fmt.Errorf("invalid previous storage encryption key")
		}
		store.keys[id] = crypto.GenerateKey(secret)
	}
	store.keyVersion = version
	store.encryptKey = crypto.GenerateKey(active)
	store.keys[version] = store.encryptKey
	legacy := cfg.Storage.LegacyEncryptionKey
	if legacy == "" && cfg.Storage.Driver == "local" {
		legacy = cfg.Storage.LocalKey
	}
	store.requireLegacyKey = cfg.Storage.Driver != "local"
	if legacy != "" {
		store.legacyKey = crypto.GenerateKey(legacy)
	}
	store.writeLegacy = cfg.Storage.WriteLegacyEncryption
	if store.writeLegacy && store.legacyKey == nil {
		return nil, fmt.Errorf("legacy encryption key required when STORAGE_WRITE_LEGACY=true")
	}
	return store, nil
}

var encryptedMagic = []byte("SNAPFLOW-ENC\x00")

func (s *EncryptedStorage) encrypt(data []byte) ([]byte, error) {
	if s.writeLegacy {
		return crypto.Encrypt(data, s.legacyKey)
	}
	result, err := crypto.Encrypt(data, s.encryptKey)
	if err != nil || s.keyVersion == "" {
		return result, err
	}
	header := append(append([]byte(nil), encryptedMagic...), byte(len(s.keyVersion)))
	header = append(header, s.keyVersion...)
	return append(header, result...), nil
}

func (s *EncryptedStorage) decrypt(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, encryptedMagic) {
		key := s.legacyKey
		if key == nil && s.requireLegacyKey {
			return nil, fmt.Errorf("legacy storage encryption key is required")
		}
		if key == nil {
			key = s.encryptKey
		}
		return crypto.Decrypt(data, key)
	}
	data = data[len(encryptedMagic):]
	if len(data) < 1 || int(data[0]) == 0 || len(data) < int(data[0])+1 {
		return nil, fmt.Errorf("invalid encrypted object header")
	}
	n := int(data[0])
	id := string(data[1 : 1+n])
	key, ok := s.keys[id]
	if !ok {
		return nil, fmt.Errorf("unknown storage encryption key version %q", id)
	}
	return crypto.Decrypt(data[1+n:], key)
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
	encrypted, err := s.encrypt(data)
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
	data, err := s.decrypt(encrypted)
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
