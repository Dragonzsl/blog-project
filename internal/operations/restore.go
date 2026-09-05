package operations

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

type RestoreResult struct {
	DataDir       string
	RollbackDir   string
	Manifest      BackupManifest
	RestoredFiles int
}

func RestoreBackup(ctx context.Context, archivePath, targetDataDir string, replace bool) (RestoreResult, error) {
	return restoreBackup(ctx, archivePath, targetDataDir, replace, nil)
}

func RestoreBackupWithCipher(ctx context.Context, archivePath, targetDataDir string, replace bool, cipher BackupCipher) (RestoreResult, error) {
	return restoreBackup(ctx, archivePath, targetDataDir, replace, cipher)
}

func restoreBackup(ctx context.Context, archivePath, targetDataDir string, replace bool, cipher BackupCipher) (RestoreResult, error) {
	targetDataDir, err := safeRestoreTarget(targetDataDir)
	if err != nil {
		return RestoreResult{}, err
	}
	targetInfo, targetErr := os.Lstat(targetDataDir)
	if targetErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.IsDir() {
			return RestoreResult{}, fmt.Errorf("restore target must be a real directory")
		}
		if !replace {
			return RestoreResult{}, fmt.Errorf("restore target already exists; pass --replace to preserve it as a rollback directory and continue")
		}
	} else if !os.IsNotExist(targetErr) {
		return RestoreResult{}, fmt.Errorf("inspect restore target: %w", targetErr)
	}
	targetExisted := targetErr == nil
	lock, err := AcquireDataLock(targetDataDir)
	if err != nil {
		return RestoreResult{}, err
	}
	defer lock.Close()
	verified, err := VerifyBackupWithCipher(ctx, archivePath, cipher)
	if err != nil {
		return RestoreResult{}, err
	}
	archiveForRestore, archiveCleanup, err := materializeBackupArchive(ctx, archivePath, cipher)
	if err != nil {
		return RestoreResult{}, err
	}
	defer archiveCleanup()
	parent := filepath.Dir(targetDataDir)
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(targetDataDir)+".restore-")
	if err != nil {
		return RestoreResult{}, fmt.Errorf("create restore staging directory: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, directory := range []string{"db", "media", "secrets", "themes", "plugins", "cache", "backups"} {
		if err := os.MkdirAll(filepath.Join(staging, directory), 0o700); err != nil {
			return RestoreResult{}, fmt.Errorf("prepare restore directory: %w", err)
		}
	}
	if err := lock.LinkInto(staging); err != nil {
		return RestoreResult{}, err
	}
	restoredFiles, err := extractVerifiedBackup(ctx, archiveForRestore, staging, verified.Manifest)
	if err != nil {
		return RestoreResult{}, err
	}
	if err := auditRestoredDatabase(ctx, filepath.Join(staging, filepath.FromSlash(verified.Manifest.DatabasePath)), verified.Manifest); err != nil {
		return RestoreResult{}, err
	}
	rollback := ""
	preserved := ""
	if targetExisted {
		rollback = targetDataDir + ".before-restore-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		preserved = rollback
	} else {
		preserved = targetDataDir + ".restore-lock-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	if _, err := os.Lstat(preserved); err == nil {
		return RestoreResult{}, fmt.Errorf("restore rollback directory already exists")
	} else if !os.IsNotExist(err) {
		return RestoreResult{}, err
	}
	if err := os.Rename(targetDataDir, preserved); err != nil {
		return RestoreResult{}, fmt.Errorf("preserve current data before restore: %w", err)
	}
	if err := os.Rename(staging, targetDataDir); err != nil {
		_ = os.Rename(preserved, targetDataDir)
		return RestoreResult{}, fmt.Errorf("activate restored data: %w", err)
	}
	if !targetExisted {
		_ = os.RemoveAll(preserved)
	}
	if err := syncDirectory(parent); err != nil {
		return RestoreResult{}, err
	}
	complete = true
	return RestoreResult{DataDir: targetDataDir, RollbackDir: rollback, Manifest: verified.Manifest, RestoredFiles: restoredFiles}, nil
}

func materializeBackupArchive(ctx context.Context, archivePath string, cipher BackupCipher) (string, func(), error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return "", func() {}, err
	}
	encrypted, err := encryptedBackupFile(archive)
	archive.Close()
	if err != nil {
		return "", func() {}, err
	}
	if !encrypted {
		return archivePath, func() {}, nil
	}
	if cipher == nil {
		return "", func() {}, errors.New("encrypted backup requires an encryption key")
	}
	directory, err := os.MkdirTemp("", "blog-backup-restore-")
	if err != nil {
		return "", func() {}, err
	}
	plainPath := filepath.Join(directory, "decrypted.tar.gz")
	plain, err := os.OpenFile(plainPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		os.RemoveAll(directory)
		return "", func() {}, err
	}
	archive, err = os.Open(archivePath)
	if err == nil {
		err = cipher.Decrypt(ctx, archive, plain)
	}
	if archive != nil {
		archive.Close()
	}
	if closeErr := plain.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.RemoveAll(directory)
		return "", func() {}, err
	}
	return plainPath, func() { _ = os.RemoveAll(directory) }, nil
}

func extractVerifiedBackup(ctx context.Context, archivePath, staging string, manifest BackupManifest) (int, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return 0, fmt.Errorf("open backup archive for restore: %w", err)
	}
	defer archive.Close()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return 0, fmt.Errorf("open backup compression for restore: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	expected := make(map[string]BackupEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		expected[entry.Path] = entry
	}
	extracted := make(map[string]struct{}, len(expected))
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read backup during restore: %w", err)
		}
		if header.Name == backupManifestPath {
			if _, err := io.CopyN(io.Discard, tarReader, header.Size); err != nil {
				return 0, fmt.Errorf("read manifest during restore: %w", err)
			}
			continue
		}
		entry, exists := expected[header.Name]
		if !exists || !validBackupEntryPath(header.Name) || header.Size != entry.Size {
			return 0, fmt.Errorf("backup entry %q changed after verification", header.Name)
		}
		if _, duplicate := extracted[header.Name]; duplicate {
			return 0, fmt.Errorf("duplicate backup entry %q during restore", header.Name)
		}
		destination := filepath.Join(staging, filepath.FromSlash(header.Name))
		inside, err := pathInside(staging, destination)
		if err != nil || !inside {
			return 0, fmt.Errorf("unsafe restore destination %q", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return 0, err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return 0, fmt.Errorf("create restored file %q: %w", header.Name, err)
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, hash), tarReader, header.Size)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		if closeErr := file.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return 0, fmt.Errorf("restore backup entry %q: %w", header.Name, copyErr)
		}
		if written != header.Size {
			return 0, fmt.Errorf("restore backup entry %q size mismatch", header.Name)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), entry.SHA256) {
			return 0, fmt.Errorf("backup checksum changed for %q during restore", header.Name)
		}
		extracted[header.Name] = struct{}{}
	}
	if len(extracted) != len(expected) {
		return 0, fmt.Errorf("restore extracted %d files, want %d", len(extracted), len(expected))
	}
	return len(extracted), nil
}

func auditRestoredDatabase(ctx context.Context, databasePath string, manifest BackupManifest) error {
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return err
	}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Set("_foreign_keys", "on")
	handle, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}).String())
	if err != nil {
		return fmt.Errorf("open restored database for audit: %w", err)
	}
	defer handle.Close()
	publicID, err := platformid.DecodePublicID(manifest.PublicID)
	if err != nil {
		return err
	}
	contextJSON, _ := json.Marshal(map[string]any{"source_migration_version": manifest.MigrationVersion, "source_application_version": manifest.ApplicationVersion})
	if _, err := handle.ExecContext(ctx, `INSERT INTO audit_entries(action,object_kind,object_public_id,result,context_json,created_at) VALUES('operations.restore.completed','backup',?,'succeeded',?,?)`, publicID, string(contextJSON), time.Now().UTC().UnixMilli()); err != nil {
		return fmt.Errorf("audit restored database: %w", err)
	}
	return nil
}

func safeRestoreTarget(path string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("restore target data directory is required")
	}
	if absolute == string(filepath.Separator) || filepath.Dir(absolute) == absolute {
		return "", fmt.Errorf("refuse to restore over a filesystem root")
	}
	for _, broad := range []string{"/tmp", "/var", "/usr", "/etc", "/home", "/Users"} {
		if absolute == broad {
			return "", fmt.Errorf("refuse to restore over broad system directory %q", absolute)
		}
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if normalizedHome, normalizeErr := filepath.Abs(home); normalizeErr == nil && absolute == normalizedHome {
			return "", fmt.Errorf("refuse to restore over the user home directory")
		}
	}
	if working, workingErr := os.Getwd(); workingErr == nil {
		if normalizedWorking, normalizeErr := filepath.Abs(working); normalizeErr == nil && absolute == normalizedWorking {
			return "", fmt.Errorf("refuse to restore over the current working directory")
		}
	}
	return absolute, nil
}
