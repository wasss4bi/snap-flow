package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// localObjects uses a private application-owned directory. Existing symlinks in
// object paths are rejected; untrusted processes must not modify this directory.
type localObjects struct {
	root string
}

func NewLocalStorage(root, secret string) (*EncryptedStorage, error) {
	if root == "" || secret == "" {
		return nil, fmt.Errorf("local storage requires a directory and LOCAL_STORAGE_KEY")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("invalid local storage directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0700); err != nil {
		return nil, fmt.Errorf("failed to create local storage directory: %w", err)
	}
	// Resolve the configured root once; paths below it must not be symlinks.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve local storage directory: %w", err)
	}
	objects := &localObjects{root: resolved}
	for _, dir := range []string{"private", "private/uploads", "private/thumbnails", "private/staging"} {
		path := filepath.Join(resolved, filepath.FromSlash(dir))
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("failed to create image directory: %w", err)
		}
		if err := checkDirectory(path); err != nil {
			return nil, err
		}
	}
	return newEncryptedStorage(objects, secret), nil
}

func checkDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("local storage path must be a directory without symlinks")
	}
	return nil
}

func (s *localObjects) objectPath(key string) (string, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != "private" ||
		(parts[1] != "uploads" && parts[1] != "thumbnails" && parts[1] != "staging") {
		return "", fmt.Errorf("invalid local object key")
	}
	if _, err := imageKey(parts[2], false); err != nil {
		return "", err
	}
	// IsLocal also rejects Windows device names. Trailing dots/spaces could
	// otherwise alias a different filename on Windows.
	if !filepath.IsLocal(parts[2]) || strings.TrimRight(parts[2], " .") != parts[2] {
		return "", fmt.Errorf("invalid local filename")
	}
	path := s.root
	for _, dir := range []string{"", parts[0], parts[1]} {
		path = filepath.Join(path, dir)
		if err := checkDirectory(path); err != nil {
			return "", err
		}
	}
	path = filepath.Join(path, parts[2])
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("local object must be a regular file without symlinks")
	}
	return path, nil
}

func (s *localObjects) put(key string, data []byte) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	// A replacement never truncates an existing object or follows its symlink.
	file, err := os.CreateTemp(filepath.Dir(path), ".image-*")
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		os.Remove(file.Name())
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (s *localObjects) get(key string) ([]byte, error) {
	path, err := s.objectPath(key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s *localObjects) delete(key string) error {
	path, err := s.objectPath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
