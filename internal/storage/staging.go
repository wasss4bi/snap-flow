package storage

import (
	"context"
	"errors"
	"snap-flow-go/pkg/crypto"
	"snap-flow-go/pkg/image"
)

type StagingStorage interface {
	Stage([]byte, string) error
	ReadStaged(string) ([]byte, error)
	DeleteStaged(string) error
}

func stagingKey(name string) (string, error) {
	if _, err := imageKey(name, false); err != nil {
		return "", err
	}
	return "private/staging/" + name, nil
}
func (s *EncryptedStorage) Stage(data []byte, name string) error {
	return s.StageContext(context.Background(), data, name)
}

// StageContext propagates cancellation when the object backend supports it.
func (s *EncryptedStorage) StageContext(ctx context.Context, data []byte, name string) error {
	if len(data) > image.MaxImageSize {
		return errors.New("staged image exceeds limit")
	}
	key, err := stagingKey(name)
	if err != nil {
		return err
	}
	data, err = crypto.Encrypt(data, s.encryptKey)
	if err != nil {
		return err
	}
	if backend, ok := s.objects.(interface {
		putContext(context.Context, string, []byte) error
	}); ok {
		return backend.putContext(ctx, key, data)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.objects.put(key, data)
}

func StageContext(ctx context.Context, stage StagingStorage, data []byte, name string) error {
	if contextual, ok := stage.(interface {
		StageContext(context.Context, []byte, string) error
	}); ok {
		return contextual.StageContext(ctx, data, name)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return stage.Stage(data, name)
}
func (s *EncryptedStorage) ReadStaged(name string) ([]byte, error) {
	key, err := stagingKey(name)
	if err != nil {
		return nil, err
	}
	raw, err := s.objects.get(key)
	if err != nil {
		return nil, err
	}
	return crypto.Decrypt(raw, s.encryptKey)
}
func (s *EncryptedStorage) DeleteStaged(name string) error {
	key, err := stagingKey(name)
	if err != nil {
		return err
	}
	return s.objects.delete(key)
}
