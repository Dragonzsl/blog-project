package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type memoryStorage struct {
	mu      sync.Mutex
	name    string
	objects map[string][]byte
}

func newMemoryStorage(name string) *memoryStorage {
	return &memoryStorage{name: name, objects: map[string][]byte{}}
}
func (m *memoryStorage) Name() string { return m.name }
func (m *memoryStorage) Put(_ context.Context, key string, source io.Reader, size int64) error {
	value, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	if size >= 0 && int64(len(value)) != size {
		return io.ErrShortBuffer
	}
	m.mu.Lock()
	m.objects[key] = append([]byte(nil), value...)
	m.mu.Unlock()
	return nil
}
func (m *memoryStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	value, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}
func (m *memoryStorage) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

func TestMigrateStorageIsChecksumVerifiedAndReversible(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(root, "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local, err := NewLocalStorage(filepath.Join(root, "media"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("original media bytes")
	key := "ab/test/original.txt"
	if err := local.Put(ctx, key, bytes.NewReader(content), int64(len(content))); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	now := time.Now().UnixMilli()
	result, err := db.Writer.ExecContext(ctx, `INSERT INTO media(public_id,original_name,mime_type,size_bytes,content_hash,object_key,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, bytes.Repeat([]byte{1}, 16), "original.txt", "text/plain", len(content), hash[:], key, now, now)
	if err != nil {
		t.Fatal(err)
	}
	mediaID, _ := result.LastInsertId()
	if _, err := db.Writer.ExecContext(ctx, `INSERT INTO media_storage_locations(media_id,variant_key,adapter,object_key,size_bytes,content_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, mediaID, "original", "local", key, len(content), hash[:], now, now); err != nil {
		t.Fatal(err)
	}
	remote := newMemoryStorage("s3")
	migrated, err := MigrateStorage(ctx, db, local, remote)
	if err != nil || migrated.CopiedObjects != 1 {
		t.Fatalf("migrated=%+v err=%v", migrated, err)
	}
	var adapter string
	if err := db.Reader.QueryRowContext(ctx, "SELECT adapter FROM media_storage_locations WHERE media_id=?", mediaID).Scan(&adapter); err != nil || adapter != "s3" {
		t.Fatalf("adapter=%s err=%v", adapter, err)
	}
	back, err := MigrateStorage(ctx, db, remote, local)
	if err != nil || back.CopiedObjects != 1 {
		t.Fatalf("back=%+v err=%v", back, err)
	}
	reader, err := local.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	reader.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("content=%q", got)
	}
}
