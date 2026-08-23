package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrDataLocked = errors.New("blog data directory is in use")

type DataLock struct {
	file *os.File
	path string
}

func AcquireDataLock(dataDir string) (*DataLock, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil || absolute == string(filepath.Separator) {
		return nil, fmt.Errorf("data directory is invalid")
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	lockPath := filepath.Join(absolute, ".blog.lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrDataLocked
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	return &DataLock{file: file, path: lockPath}, nil
}

func (lock *DataLock) LinkInto(dataDir string) error {
	if lock == nil || lock.file == nil {
		return fmt.Errorf("data directory lock is not held")
	}
	destination := filepath.Join(dataDir, ".blog.lock")
	if err := os.Link(lock.path, destination); err != nil {
		return fmt.Errorf("carry data directory lock into restored data: %w", err)
	}
	return nil
}

func (lock *DataLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	lock.path = ""
	if unlockErr != nil {
		return fmt.Errorf("unlock data directory: %w", unlockErr)
	}
	return closeErr
}
