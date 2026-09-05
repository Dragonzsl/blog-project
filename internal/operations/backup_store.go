package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zhushilin/blog-project/internal/media"
)

type BackupObject struct {
	Key  string
	Size int64
}

// BackupStore is deliberately independent from the media lifecycle. A store
// only moves a completed archive and never receives database credentials or
// backup keys.
type BackupStore interface {
	Name() string
	Put(context.Context, string, io.Reader, int64) error
	Open(context.Context, string) (io.ReadCloser, error)
	Stat(context.Context, string) (BackupObject, error)
}

type backupStoreDeleter interface {
	Delete(context.Context, string) error
}

type LocalBackupStore struct{ Root string }

func NewLocalBackupStore(root string) (*LocalBackupStore, error) {
	root, err := filepath.Abs(root)
	if err != nil || root == string(filepath.Separator) {
		return nil, errors.New("local backup store root is invalid")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create local backup store: %w", err)
	}
	return &LocalBackupStore{Root: root}, nil
}

func (s *LocalBackupStore) Name() string { return "local" }

func (s *LocalBackupStore) safePath(key string) (string, error) {
	key = strings.Trim(strings.ReplaceAll(key, "\\", "/"), "/")
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") || strings.Contains(key, "\x00") {
		return "", errors.New("invalid backup object key")
	}
	file := filepath.Join(s.Root, filepath.FromSlash(key))
	inside, err := pathInside(s.Root, file)
	if err != nil || !inside {
		return "", errors.New("invalid backup object key")
	}
	return file, nil
}

func (s *LocalBackupStore) Put(ctx context.Context, key string, source io.Reader, size int64) error {
	if size < 0 || size > maxBackupExtractSize {
		return errors.New("backup object size is outside the allowed range")
	}
	file, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(file), ".backup-object-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	written, err := io.Copy(temporary, io.LimitReader(source, size+1))
	if err == nil && written != size {
		err = fmt.Errorf("backup object size mismatch: got %d, want %d", written, size)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(file); err == nil {
		return errors.New("backup object already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temporaryPath, file)
}

func (s *LocalBackupStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	file, err := s.safePath(key)
	if err != nil {
		return nil, err
	}
	return os.Open(file)
}

func (s *LocalBackupStore) Stat(_ context.Context, key string) (BackupObject, error) {
	file, err := s.safePath(key)
	if err != nil {
		return BackupObject{}, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return BackupObject{}, err
	}
	if !info.Mode().IsRegular() {
		return BackupObject{}, errors.New("backup object is not a regular file")
	}
	return BackupObject{Key: key, Size: info.Size()}, nil
}

func (s *LocalBackupStore) Delete(_ context.Context, key string) error {
	file, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MediaStorageBackupStore adapts the existing timeout-bounded media storage
// client for an independent backup prefix. It intentionally does not expose
// media object keys as backup object keys.
type MediaStorageBackupStore struct {
	storage media.Storage
	prefix  string
}

func NewMediaStorageBackupStore(storage media.Storage, prefix string) (*MediaStorageBackupStore, error) {
	if storage == nil || storage.Name() == "local" {
		return nil, errors.New("a non-local media storage adapter is required")
	}
	prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
	if prefix == "" || path.Clean(prefix) != prefix || strings.HasPrefix(prefix, "../") {
		return nil, errors.New("backup storage prefix is invalid")
	}
	return &MediaStorageBackupStore{storage: storage, prefix: prefix}, nil
}

func (s *MediaStorageBackupStore) Name() string { return s.storage.Name() }

func (s *MediaStorageBackupStore) objectKey(key string) (string, error) {
	key = strings.Trim(strings.ReplaceAll(key, "\\", "/"), "/")
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") {
		return "", errors.New("invalid backup object key")
	}
	return s.prefix + "/" + key, nil
}

func (s *MediaStorageBackupStore) Put(ctx context.Context, key string, source io.Reader, size int64) error {
	if size < 0 || size > maxBackupExtractSize {
		return errors.New("backup object size is outside the allowed range")
	}
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	return s.storage.Put(ctx, objectKey, source, size)
}

func (s *MediaStorageBackupStore) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return nil, err
	}
	return s.storage.Open(ctx, objectKey)
}

func (s *MediaStorageBackupStore) Stat(ctx context.Context, key string) (BackupObject, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return BackupObject{}, err
	}
	reader, err := s.storage.Open(ctx, objectKey)
	if err != nil {
		return BackupObject{}, err
	}
	defer reader.Close()
	size, err := io.Copy(io.Discard, io.LimitReader(reader, maxBackupExtractSize+1))
	if err != nil {
		return BackupObject{}, err
	}
	if size > maxBackupExtractSize {
		return BackupObject{}, errors.New("backup object is too large")
	}
	return BackupObject{Key: key, Size: size}, nil
}

func (s *MediaStorageBackupStore) Delete(ctx context.Context, key string) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	return s.storage.Delete(ctx, objectKey)
}
