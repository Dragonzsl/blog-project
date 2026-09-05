package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/database"
)

// Storage is deliberately small so a plugin or a migration can provide an
// in-memory, filesystem, or S3-compatible implementation without changing
// media metadata semantics.
type Storage interface {
	Name() string
	Put(context.Context, string, io.Reader, int64) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

type LocalStorage struct{ Root string }

func NewLocalStorage(root string) (*LocalStorage, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create local media storage: %w", err)
	}
	return &LocalStorage{Root: root}, nil
}

func (s *LocalStorage) Name() string { return "local" }

func (s *LocalStorage) safePath(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "\x00") {
		return "", errors.New("invalid storage object key")
	}
	clean := path.Clean(key)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid storage object key")
	}
	file := filepath.Join(s.Root, filepath.FromSlash(clean))
	relative, err := filepath.Rel(s.Root, file)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid storage object key")
	}
	return file, nil
}

func (s *LocalStorage) Put(ctx context.Context, key string, source io.Reader, size int64) error {
	file, err := s.safePath(key)
	if err != nil {
		return err
	}
	if size < 0 {
		size = 0
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(file), ".object-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	written, err := io.Copy(temporary, io.LimitReader(source, size+1))
	if err != nil {
		temporary.Close()
		return err
	}
	if size >= 0 && written != size {
		temporary.Close()
		return fmt.Errorf("object size mismatch: got %d, want %d", written, size)
	}
	if err := ctx.Err(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, file)
}

func (s *LocalStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	file, err := s.safePath(key)
	if err != nil {
		return nil, err
	}
	return os.Open(file)
}

func (s *LocalStorage) Delete(_ context.Context, key string) error {
	file, err := s.safePath(key)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

type S3Storage struct {
	Endpoint       *url.URL
	Bucket         string
	Region         string
	AccessKey      string
	SecretKey      string
	Prefix         string
	ForcePathStyle bool
	UseTLS         bool
	Client         *http.Client
}

func NewS3Storage(endpoint, bucket, region, accessKey, secretKey, prefix string, forcePathStyle, useTLS bool) (*S3Storage, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid S3 endpoint")
	}
	if useTLS && parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	if !useTLS && parsed.Scheme == "https" {
		parsed.Scheme = "http"
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, errors.New("S3 bucket is required")
	}
	return &S3Storage{Endpoint: parsed, Bucket: bucket, Region: region, AccessKey: accessKey, SecretKey: secretKey, Prefix: strings.Trim(prefix, "/"), ForcePathStyle: forcePathStyle, UseTLS: useTLS, Client: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (s *S3Storage) Name() string { return "s3" }

func (s *S3Storage) objectURL(key string) (*url.URL, error) {
	key = strings.Trim(strings.ReplaceAll(key, "\\", "/"), "/")
	if key == "" || path.Clean(key) != key || strings.HasPrefix(key, "../") || strings.Contains(key, "\x00") {
		return nil, errors.New("invalid storage object key")
	}
	if s.Prefix != "" {
		key = s.Prefix + "/" + key
	}
	result := *s.Endpoint
	if s.ForcePathStyle {
		result.Path = strings.TrimRight(result.Path, "/") + "/" + url.PathEscape(s.Bucket) + "/" + escapeObjectKey(key)
	} else {
		result.Host = s.Bucket + "." + result.Host
		result.Path = strings.TrimRight(result.Path, "/") + "/" + escapeObjectKey(key)
	}
	return &result, nil
}

func escapeObjectKey(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (s *S3Storage) request(ctx context.Context, method, key string, body io.Reader, size int64) (*http.Response, error) {
	location, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, location.String(), body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		request.ContentLength = size
	}
	if s.AccessKey != "" {
		if err := s.signRequest(request, "UNSIGNED-PAYLOAD"); err != nil {
			return nil, err
		}
	}
	return s.Client.Do(request)
}

func (s *S3Storage) signRequest(request *http.Request, payloadHash string) error {
	if strings.TrimSpace(s.AccessKey) == "" || strings.TrimSpace(s.SecretKey) == "" {
		return nil
	}
	now := time.Now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	request.Header.Set("Host", request.URL.Host)
	request.Host = request.URL.Host
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	canonicalURI := request.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalHeaders := "host:" + request.URL.Host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalQuery := request.URL.Query().Encode()
	canonicalRequest := strings.Join([]string{request.Method, canonicalURI, canonicalQuery, canonicalHeaders, signedHeaders, payloadHash}, "\n")
	credentialScope := date + "/" + s.Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + hex.EncodeToString(sha256Bytes([]byte(canonicalRequest)))
	dateKey := hmacSHA256([]byte("AWS4"+s.SecretKey), []byte(date))
	regionKey := hmacSHA256(dateKey, []byte(s.Region))
	serviceKey := hmacSHA256(regionKey, []byte("s3"))
	signingKey := hmacSHA256(serviceKey, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	return nil
}

func hmacSHA256(key, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
func sha256Bytes(value []byte) []byte { digest := sha256.Sum256(value); return digest[:] }

func (s *S3Storage) Put(ctx context.Context, key string, source io.Reader, size int64) error {
	response, err := s.request(ctx, http.MethodPut, key, source, size)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("S3 PUT returned %s", response.Status)
	}
	return nil
}

func (s *S3Storage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	response, err := s.request(ctx, http.MethodGet, key, nil, 0)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, fmt.Errorf("S3 GET returned %s", response.Status)
	}
	return response.Body, nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	response, err := s.request(ctx, http.MethodDelete, key, nil, 0)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound && (response.StatusCode < 200 || response.StatusCode >= 300) {
		return fmt.Errorf("S3 DELETE returned %s", response.Status)
	}
	return nil
}

type StorageMigrationResult struct {
	ID            int64
	SourceAdapter string
	Destination   string
	TotalObjects  int
	CopiedObjects int
	FailedObjects int
}

const storageMigrationBatchSize = 16

type storageMigrationObject struct {
	mediaID int64
	variant string
	key     string
	size    int64
	hash    []byte
}

// MigrateStorage copies media objects in fixed-size batches, verifies size and
// SHA-256 at both ends, and flips the location index only after all copies
// pass. Progress is persisted after every confirmed object, so a failed run
// can resume without retaining the complete media inventory in memory.
func MigrateStorage(ctx context.Context, db *database.DB, source, destination Storage) (StorageMigrationResult, error) {
	if db == nil || source == nil || destination == nil {
		return StorageMigrationResult{}, errors.New("database and storage adapters are required")
	}
	if source.Name() == destination.Name() {
		return StorageMigrationResult{}, errors.New("source and destination storage adapters must differ")
	}
	var total int
	if err := db.Reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM media_storage_locations WHERE adapter=?", source.Name()).Scan(&total); err != nil {
		return StorageMigrationResult{}, err
	}
	now := time.Now().UTC()
	result := StorageMigrationResult{SourceAdapter: source.Name(), Destination: destination.Name(), TotalObjects: total}
	var cursorMediaID int64
	var cursorVariant string
	err := db.Reader.QueryRowContext(ctx, `SELECT id,cursor_media_id,cursor_variant_key,copied_objects FROM storage_migrations WHERE source_adapter=? AND destination_adapter=? AND status IN ('running','failed') ORDER BY started_at DESC,id DESC LIMIT 1`, source.Name(), destination.Name()).Scan(&result.ID, &cursorMediaID, &cursorVariant, &result.CopiedObjects)
	if errors.Is(err, sql.ErrNoRows) {
		insert, insertErr := db.Writer.ExecContext(ctx, `INSERT INTO storage_migrations(source_adapter,destination_adapter,status,total_objects,batch_size,started_at,lease_expires_at) VALUES(?,?, 'running',?,?,?,?)`, source.Name(), destination.Name(), total, storageMigrationBatchSize, now.UnixMilli(), now.Add(2*time.Minute).UnixMilli())
		if insertErr != nil {
			return result, insertErr
		}
		result.ID, _ = insert.LastInsertId()
	} else if err != nil {
		return result, err
	} else {
		if _, err := db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET status='running',total_objects=?,batch_size=?,last_error='',completed_at=NULL,lease_expires_at=? WHERE id=?`, total, storageMigrationBatchSize, now.Add(2*time.Minute).UnixMilli(), result.ID); err != nil {
			return result, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			result.FailedObjects++
			return result, finishMigration(ctx, db, result.ID, result, err)
		}
		rows, err := db.Reader.QueryContext(ctx, `
			SELECT media_id,variant_key,object_key,size_bytes,content_hash
			FROM media_storage_locations
			WHERE adapter=? AND (media_id>? OR (media_id=? AND variant_key>?))
			ORDER BY media_id,variant_key LIMIT ?`, source.Name(), cursorMediaID, cursorMediaID, cursorVariant, storageMigrationBatchSize)
		if err != nil {
			return result, finishMigration(ctx, db, result.ID, result, err)
		}
		batch := make([]storageMigrationObject, 0, storageMigrationBatchSize)
		for rows.Next() {
			var item storageMigrationObject
			if err := rows.Scan(&item.mediaID, &item.variant, &item.key, &item.size, &item.hash); err != nil {
				rows.Close()
				return result, finishMigration(ctx, db, result.ID, result, err)
			}
			batch = append(batch, item)
		}
		if err := rows.Close(); err != nil {
			return result, finishMigration(ctx, db, result.ID, result, err)
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if err := copyStorageObject(ctx, source, destination, item); err != nil {
				result.FailedObjects++
				return result, finishMigration(ctx, db, result.ID, result, err)
			}
			result.CopiedObjects++
			cursorMediaID, cursorVariant = item.mediaID, item.variant
			if _, err := db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET copied_objects=?,cursor_media_id=?,cursor_variant_key=?,lease_expires_at=? WHERE id=?`, result.CopiedObjects, cursorMediaID, cursorVariant, time.Now().UTC().Add(2*time.Minute).UnixMilli(), result.ID); err != nil {
				return result, finishMigration(ctx, db, result.ID, result, err)
			}
		}
	}
	if _, err := db.Writer.ExecContext(ctx, `UPDATE media_storage_locations SET adapter=?,updated_at=? WHERE adapter=?`, destination.Name(), time.Now().UTC().UnixMilli(), source.Name()); err != nil {
		return result, finishMigration(ctx, db, result.ID, result, err)
	}
	_, err = db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET status='succeeded',copied_objects=?,failed_objects=0,completed_at=?,lease_expires_at=NULL,last_error='' WHERE id=?`, result.CopiedObjects, time.Now().UTC().UnixMilli(), result.ID)
	return result, err
}

func copyStorageObject(ctx context.Context, source, destination Storage, item storageMigrationObject) error {
	reader, err := source.Open(ctx, item.key)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp("", "blog-storage-migrate-*")
	if err != nil {
		reader.Close()
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	defer temporary.Close()
	written, copyErr := io.Copy(temporary, io.LimitReader(reader, item.size+1))
	closeErr := reader.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if written != item.size {
		return errors.New("storage source size mismatch")
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, temporary); err != nil {
		return err
	}
	if !equalBytes(hash.Sum(nil), item.hash) {
		return errors.New("storage source checksum mismatch")
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := destination.Put(ctx, item.key, temporary, item.size); err != nil {
		return err
	}
	copied, err := destination.Open(ctx, item.key)
	if err != nil {
		return err
	}
	defer copied.Close()
	hash.Reset()
	copiedSize, err := io.Copy(hash, io.LimitReader(copied, item.size+1))
	if err != nil {
		return err
	}
	if copiedSize != item.size || !equalBytes(hash.Sum(nil), item.hash) {
		return errors.New("destination storage checksum mismatch")
	}
	return nil
}

func finishMigration(ctx context.Context, db *database.DB, id int64, result StorageMigrationResult, failure error) error {
	message := failure.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	_, _ = db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET status='failed',copied_objects=?,failed_objects=?,last_error=?,completed_at=?,lease_expires_at=NULL WHERE id=?`, result.CopiedObjects, result.FailedObjects, message, time.Now().UTC().UnixMilli(), id)
	return failure
}

func temporarySize(file *os.File) int64 {
	info, err := file.Stat()
	if err != nil {
		return -1
	}
	return info.Size()
}

func equalBytes(left, right []byte) bool {
	return hex.EncodeToString(left) == hex.EncodeToString(right)
}
