package media

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

// MigrateStorage copies every media object recorded in the location index,
// verifies size and SHA-256, and flips the index only after all copies pass.
// A failed run leaves the source locations untouched and can be retried.
func MigrateStorage(ctx context.Context, db *database.DB, source, destination Storage) (StorageMigrationResult, error) {
	if db == nil || source == nil || destination == nil {
		return StorageMigrationResult{}, errors.New("database and storage adapters are required")
	}
	rows, err := db.Reader.QueryContext(ctx, `SELECT media_id,variant_key,object_key,size_bytes,content_hash FROM media_storage_locations WHERE adapter=? ORDER BY media_id,variant_key`, source.Name())
	if err != nil {
		return StorageMigrationResult{}, err
	}
	type object struct {
		mediaID int64
		variant string
		key     string
		size    int64
		hash    []byte
	}
	var objects []object
	for rows.Next() {
		var item object
		if err := rows.Scan(&item.mediaID, &item.variant, &item.key, &item.size, &item.hash); err != nil {
			rows.Close()
			return StorageMigrationResult{}, err
		}
		objects = append(objects, item)
	}
	if err := rows.Close(); err != nil {
		return StorageMigrationResult{}, err
	}
	now := time.Now().UTC().UnixMilli()
	result := StorageMigrationResult{SourceAdapter: source.Name(), Destination: destination.Name(), TotalObjects: len(objects)}
	insert, err := db.Writer.ExecContext(ctx, `INSERT INTO storage_migrations(source_adapter,destination_adapter,status,total_objects,started_at) VALUES(?,?, 'running',?,?)`, source.Name(), destination.Name(), len(objects), now)
	if err != nil {
		return result, err
	}
	result.ID, _ = insert.LastInsertId()
	for _, item := range objects {
		reader, openErr := source.Open(ctx, item.key)
		if openErr != nil {
			result.FailedObjects++
			return result, finishMigration(ctx, db, result.ID, result, openErr)
		}
		temporary, tempErr := os.CreateTemp("", "blog-storage-migrate-*")
		if tempErr == nil {
			_, tempErr = io.Copy(temporary, io.LimitReader(reader, item.size+1))
		}
		closeErr := reader.Close()
		if tempErr == nil {
			tempErr = closeErr
		}
		if tempErr == nil {
			if _, tempErr = temporary.Seek(0, io.SeekStart); tempErr == nil {
				hash := sha256.New()
				_, tempErr = io.Copy(hash, temporary)
				if tempErr == nil && (temporarySize(temporary) != item.size || !equalBytes(hash.Sum(nil), item.hash)) {
					tempErr = errors.New("storage checksum mismatch")
				}
			}
		}
		if tempErr == nil {
			if _, tempErr = temporary.Seek(0, io.SeekStart); tempErr == nil {
				tempErr = destination.Put(ctx, item.key, temporary, item.size)
			}
		}
		if tempErr == nil {
			var copied io.ReadCloser
			copied, tempErr = destination.Open(ctx, item.key)
			if tempErr == nil {
				hash := sha256.New()
				var copiedSize int64
				copiedSize, tempErr = io.Copy(hash, io.LimitReader(copied, item.size+1))
				closeErr := copied.Close()
				if tempErr == nil {
					tempErr = closeErr
				}
				if tempErr == nil && (copiedSize != item.size || !equalBytes(hash.Sum(nil), item.hash)) {
					tempErr = errors.New("destination storage checksum mismatch")
				}
			}
		}
		if temporary != nil {
			temporary.Close()
			os.Remove(temporary.Name())
		}
		if tempErr != nil {
			result.FailedObjects++
			return result, finishMigration(ctx, db, result.ID, result, tempErr)
		}
		result.CopiedObjects++
	}
	tx, err := db.Writer.BeginTx(ctx, nil)
	if err != nil {
		return result, finishMigration(ctx, db, result.ID, result, err)
	}
	for _, item := range objects {
		if _, err := tx.ExecContext(ctx, `UPDATE media_storage_locations SET adapter=?,updated_at=? WHERE media_id=? AND variant_key=?`, destination.Name(), now, item.mediaID, item.variant); err != nil {
			tx.Rollback()
			return result, finishMigration(ctx, db, result.ID, result, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return result, finishMigration(ctx, db, result.ID, result, err)
	}
	_, err = db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET status='succeeded',copied_objects=?,failed_objects=0,completed_at=? WHERE id=?`, result.CopiedObjects, time.Now().UTC().UnixMilli(), result.ID)
	return result, err
}

func finishMigration(ctx context.Context, db *database.DB, id int64, result StorageMigrationResult, failure error) error {
	_, _ = db.Writer.ExecContext(ctx, `UPDATE storage_migrations SET status='failed',copied_objects=?,failed_objects=?,last_error=?,completed_at=? WHERE id=?`, result.CopiedObjects, result.FailedObjects, failure.Error(), time.Now().UTC().UnixMilli(), id)
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
