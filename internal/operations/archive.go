package operations

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
	platformid "github.com/zhushilin/blog-project/internal/platform/id"
)

const (
	backupFormat         = "personal-blog-backup"
	backupFormatVersion  = 1
	backupManifestPath   = "manifest.json"
	maxBackupEntries     = 100_000
	maxBackupManifest    = 8 << 20
	maxBackupExtractSize = int64(100 << 30)
)

type BackupManifest struct {
	Format             string        `json:"format"`
	FormatVersion      int           `json:"format_version"`
	PublicID           string        `json:"public_id"`
	Reason             string        `json:"reason"`
	CreatedAt          time.Time     `json:"created_at"`
	ApplicationVersion string        `json:"application_version"`
	ApplicationCommit  string        `json:"application_commit"`
	MigrationVersion   int64         `json:"migration_version"`
	DatabasePath       string        `json:"database_path"`
	Entries            []BackupEntry `json:"entries"`
}

type BackupEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type archiveSource struct {
	archivePath string
	filePath    string
	size        int64
}

type VerifiedBackup struct {
	Manifest  BackupManifest
	SizeBytes int64
}

func writeBackupArchive(ctx context.Context, output string, manifest BackupManifest, sources []archiveSource) error {
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create backup archive: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(output)
		}
	}()
	gzipWriter, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		return fmt.Errorf("create backup compressor: %w", err)
	}
	tarWriter := tar.NewWriter(gzipWriter)
	sort.Slice(sources, func(i, j int) bool { return sources[i].archivePath < sources[j].archivePath })
	manifest.Entries = make([]BackupEntry, 0, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validBackupEntryPath(source.archivePath) {
			return fmt.Errorf("unsafe backup entry path %q", source.archivePath)
		}
		header := &tar.Header{Name: source.archivePath, Mode: 0o600, Size: source.size, ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("write backup header %q: %w", source.archivePath, err)
		}
		input, err := os.Open(source.filePath)
		if err != nil {
			return fmt.Errorf("open backup source %q: %w", source.archivePath, err)
		}
		hash := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(tarWriter, hash), input, source.size)
		closeErr := input.Close()
		if copyErr != nil {
			return fmt.Errorf("stream backup source %q: %w", source.archivePath, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close backup source %q: %w", source.archivePath, closeErr)
		}
		if written != source.size {
			return fmt.Errorf("backup source %q changed while being read", source.archivePath)
		}
		manifest.Entries = append(manifest.Entries, BackupEntry{Path: source.archivePath, Size: source.size, SHA256: hex.EncodeToString(hash.Sum(nil))})
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := tarWriter.WriteHeader(&tar.Header{Name: backupManifestPath, Mode: 0o600, Size: int64(len(manifestJSON)), ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
		return fmt.Errorf("write backup manifest header: %w", err)
	}
	if _, err := tarWriter.Write(manifestJSON); err != nil {
		return fmt.Errorf("write backup manifest: %w", err)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("finish backup archive: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("finish backup compression: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync backup archive: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close backup archive: %w", err)
	}
	complete = true
	return nil
}

func VerifyBackup(ctx context.Context, archivePath string) (VerifiedBackup, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return VerifiedBackup{}, fmt.Errorf("open backup archive: %w", err)
	}
	info, err := archive.Stat()
	if err != nil {
		archive.Close()
		return VerifiedBackup{}, fmt.Errorf("inspect backup archive: %w", err)
	}
	temporary, err := os.MkdirTemp(filepath.Dir(archivePath), ".blog-backup-verify-")
	if err != nil {
		temporary, err = os.MkdirTemp("", "blog-backup-verify-")
	}
	if err != nil {
		archive.Close()
		return VerifiedBackup{}, fmt.Errorf("create backup verification directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	result, err := verifyBackupReader(ctx, archive, info.Size(), temporary)
	closeErr := archive.Close()
	if err != nil {
		return VerifiedBackup{}, err
	}
	if closeErr != nil {
		return VerifiedBackup{}, fmt.Errorf("close backup archive: %w", closeErr)
	}
	return result, nil
}

func verifyBackupReader(ctx context.Context, input io.Reader, archiveSize int64, temporary string) (VerifiedBackup, error) {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return VerifiedBackup{}, fmt.Errorf("open backup compression: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	observed := make(map[string]BackupEntry)
	var manifest BackupManifest
	manifestSeen := false
	databaseCandidates := make(map[string]string)
	var totalSize int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return VerifiedBackup{}, err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return VerifiedBackup{}, fmt.Errorf("read backup archive: %w", err)
		}
		if count >= maxBackupEntries {
			return VerifiedBackup{}, fmt.Errorf("backup contains more than %d entries", maxBackupEntries)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return VerifiedBackup{}, fmt.Errorf("backup entry %q is not a regular file", header.Name)
		}
		if header.Size < 0 || totalSize > maxBackupExtractSize-header.Size {
			return VerifiedBackup{}, fmt.Errorf("backup extracted size exceeds %d bytes", maxBackupExtractSize)
		}
		totalSize += header.Size
		if header.Name == backupManifestPath {
			if manifestSeen || header.Size > maxBackupManifest {
				return VerifiedBackup{}, fmt.Errorf("backup manifest is duplicate or too large")
			}
			contents, err := io.ReadAll(io.LimitReader(tarReader, maxBackupManifest+1))
			if err != nil {
				return VerifiedBackup{}, fmt.Errorf("read backup manifest: %w", err)
			}
			if int64(len(contents)) != header.Size {
				return VerifiedBackup{}, fmt.Errorf("backup manifest size mismatch")
			}
			if err := json.Unmarshal(contents, &manifest); err != nil {
				return VerifiedBackup{}, fmt.Errorf("decode backup manifest: %w", err)
			}
			manifestSeen = true
			continue
		}
		if manifestSeen {
			return VerifiedBackup{}, fmt.Errorf("backup manifest must be the final archive entry")
		}
		if !validBackupEntryPath(header.Name) {
			return VerifiedBackup{}, fmt.Errorf("unsafe backup entry path %q", header.Name)
		}
		if _, duplicate := observed[header.Name]; duplicate {
			return VerifiedBackup{}, fmt.Errorf("duplicate backup entry %q", header.Name)
		}
		hash := sha256.New()
		writer := io.Writer(hash)
		var databaseFile *os.File
		if strings.HasPrefix(header.Name, "db/") && strings.HasSuffix(header.Name, ".sqlite") {
			databasePath := filepath.Join(temporary, fmt.Sprintf("database-%d.sqlite", len(databaseCandidates)))
			databaseFile, err = os.OpenFile(databasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return VerifiedBackup{}, fmt.Errorf("create database verification file: %w", err)
			}
			databaseCandidates[header.Name] = databasePath
			writer = io.MultiWriter(hash, databaseFile)
		}
		written, copyErr := io.CopyN(writer, tarReader, header.Size)
		if databaseFile != nil {
			if closeErr := databaseFile.Close(); copyErr == nil {
				copyErr = closeErr
			}
		}
		if copyErr != nil {
			return VerifiedBackup{}, fmt.Errorf("read backup entry %q: %w", header.Name, copyErr)
		}
		if written != header.Size {
			return VerifiedBackup{}, fmt.Errorf("backup entry %q size changed while being read", header.Name)
		}
		observed[header.Name] = BackupEntry{Path: header.Name, Size: header.Size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	if !manifestSeen {
		return VerifiedBackup{}, fmt.Errorf("backup manifest is missing")
	}
	if _, err := io.Copy(io.Discard, gzipReader); err != nil {
		return VerifiedBackup{}, fmt.Errorf("finish reading backup compression: %w", err)
	}
	if err := validateManifest(manifest, observed); err != nil {
		return VerifiedBackup{}, err
	}
	databasePath, exists := databaseCandidates[manifest.DatabasePath]
	if !exists {
		return VerifiedBackup{}, fmt.Errorf("backup database entry is missing")
	}
	version, err := database.ValidateSnapshot(ctx, databasePath)
	if err != nil {
		return VerifiedBackup{}, fmt.Errorf("verify backup database: %w", err)
	}
	if version != manifest.MigrationVersion {
		return VerifiedBackup{}, fmt.Errorf("backup database migration version is %d, manifest says %d", version, manifest.MigrationVersion)
	}
	if version > database.LatestMigrationVersion {
		return VerifiedBackup{}, fmt.Errorf("backup migration version %d is newer than supported version %d", version, database.LatestMigrationVersion)
	}
	return VerifiedBackup{Manifest: manifest, SizeBytes: archiveSize}, nil
}

func validateManifest(manifest BackupManifest, observed map[string]BackupEntry) error {
	if manifest.Format != backupFormat || manifest.FormatVersion != backupFormatVersion {
		return fmt.Errorf("unsupported backup format %q version %d", manifest.Format, manifest.FormatVersion)
	}
	if _, err := platformid.DecodePublicID(manifest.PublicID); err != nil {
		return fmt.Errorf("invalid backup public ID: %w", err)
	}
	if !validBackupReason(manifest.Reason) || manifest.CreatedAt.IsZero() {
		return fmt.Errorf("backup manifest metadata is invalid")
	}
	if !validBackupEntryPath(manifest.DatabasePath) {
		return fmt.Errorf("backup database path is invalid")
	}
	if len(manifest.Entries) != len(observed) {
		return fmt.Errorf("backup manifest lists %d files but archive contains %d", len(manifest.Entries), len(observed))
	}
	listed := make(map[string]struct{}, len(manifest.Entries))
	for _, expected := range manifest.Entries {
		if !validBackupEntryPath(expected.Path) || expected.Size < 0 || len(expected.SHA256) != sha256.Size*2 {
			return fmt.Errorf("backup manifest entry %q is invalid", expected.Path)
		}
		if _, duplicate := listed[expected.Path]; duplicate {
			return fmt.Errorf("backup manifest entry %q is duplicated", expected.Path)
		}
		listed[expected.Path] = struct{}{}
		actual, exists := observed[expected.Path]
		if !exists || actual.Size != expected.Size || !strings.EqualFold(actual.SHA256, expected.SHA256) {
			return fmt.Errorf("backup checksum mismatch for %q", expected.Path)
		}
	}
	if _, exists := listed[manifest.DatabasePath]; !exists {
		return fmt.Errorf("backup manifest does not list its database")
	}
	return nil
}

func validBackupEntryPath(path string) bool {
	if path == "" || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") || filepath.ToSlash(filepath.Clean(path)) != path {
		return false
	}
	for _, prefix := range []string{"db/", "media/", "secrets/", "themes/", "plugins/"} {
		if strings.HasPrefix(path, prefix) && len(path) > len(prefix) {
			return true
		}
	}
	return false
}

func validBackupReason(reason string) bool {
	return reason == "manual" || reason == "scheduled" || reason == "pre_upgrade"
}
