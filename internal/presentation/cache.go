package presentation

import (
	"container/list"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const maxCacheFileBytes = 4 << 20

type CacheEntry struct {
	Key          string
	Epoch        int64
	Status       int
	ContentType  string
	ETag         string
	LastModified string
	Body         []byte
}

type memoryEntry struct {
	key   string
	value CacheEntry
	size  int64
}

type PageCache struct {
	directory      string
	maxEntries     int
	maxMemoryBytes int64

	mu          sync.Mutex
	items       map[string]*list.Element
	recent      *list.List
	memoryBytes int64
}

func NewPageCache(directory string, maxEntries int, maxMemoryBytes int64) (*PageCache, error) {
	if maxEntries < 1 || maxEntries > 1024 {
		return nil, fmt.Errorf("page cache entries must be between 1 and 1024")
	}
	if maxMemoryBytes < 64<<10 || maxMemoryBytes > 128<<20 {
		return nil, fmt.Errorf("page cache memory must be between 64 KiB and 128 MiB")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create page cache directory: %w", err)
	}
	return &PageCache{
		directory:      directory,
		maxEntries:     maxEntries,
		maxMemoryBytes: maxMemoryBytes,
		items:          make(map[string]*list.Element),
		recent:         list.New(),
	}, nil
}

func (cache *PageCache) Get(key string, epoch int64) (CacheEntry, bool) {
	cache.mu.Lock()
	if element, ok := cache.items[key]; ok {
		item := element.Value.(*memoryEntry)
		if item.value.Epoch == epoch {
			cache.recent.MoveToFront(element)
			value := cloneCacheEntry(item.value)
			cache.mu.Unlock()
			return value, true
		}
		cache.removeElement(element)
	}
	cache.mu.Unlock()

	path := cache.path(key, epoch)
	info, err := os.Stat(path)
	if err != nil || info.Size() < 1 || info.Size() > maxCacheFileBytes {
		return CacheEntry{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return CacheEntry{}, false
	}
	defer file.Close()
	var value CacheEntry
	if err := gob.NewDecoder(io.LimitReader(file, maxCacheFileBytes)).Decode(&value); err != nil || value.Key != key || value.Epoch != epoch || len(value.Body) > maxCacheFileBytes {
		_ = os.Remove(path)
		return CacheEntry{}, false
	}
	cache.addMemory(key, value)
	return cloneCacheEntry(value), true
}

func (cache *PageCache) Put(value CacheEntry) error {
	if value.Key == "" || value.Epoch < 1 || len(value.Body) > maxCacheFileBytes {
		return fmt.Errorf("invalid page cache entry")
	}
	value = cloneCacheEntry(value)
	cache.addMemory(value.Key, value)

	temporary, err := os.CreateTemp(cache.directory, ".page-*.tmp")
	if err != nil {
		return fmt.Errorf("create page cache temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect page cache temporary file: %w", err)
	}
	if err := gob.NewEncoder(temporary).Encode(value); err != nil {
		temporary.Close()
		return fmt.Errorf("encode page cache: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync page cache: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close page cache: %w", err)
	}
	if err := os.Rename(temporaryPath, cache.path(value.Key, value.Epoch)); err != nil {
		return fmt.Errorf("publish page cache: %w", err)
	}
	return nil
}

func (cache *PageCache) Prune(currentEpoch int64) error {
	entries, err := os.ReadDir(cache.directory)
	if err != nil {
		return fmt.Errorf("read page cache directory: %w", err)
	}
	removed := 0
	for _, entry := range entries {
		if removed >= 100 || entry.IsDir() || !strings.HasPrefix(entry.Name(), "epoch-") || !strings.HasSuffix(entry.Name(), ".cache") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(entry.Name(), "epoch-"), "-", 2)
		if len(parts) != 2 {
			continue
		}
		epoch, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || epoch >= currentEpoch-1 {
			continue
		}
		if err := os.Remove(filepath.Join(cache.directory, entry.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale page cache: %w", err)
		}
		removed++
	}
	return nil
}

func (cache *PageCache) addMemory(key string, value CacheEntry) {
	size := int64(len(value.Body) + len(value.Key) + len(value.ETag) + 256)
	if size > cache.maxMemoryBytes {
		return
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if existing, ok := cache.items[key]; ok {
		cache.removeElement(existing)
	}
	element := cache.recent.PushFront(&memoryEntry{key: key, value: cloneCacheEntry(value), size: size})
	cache.items[key] = element
	cache.memoryBytes += size
	for cache.recent.Len() > cache.maxEntries || cache.memoryBytes > cache.maxMemoryBytes {
		cache.removeElement(cache.recent.Back())
	}
}

func (cache *PageCache) removeElement(element *list.Element) {
	if element == nil {
		return
	}
	item := element.Value.(*memoryEntry)
	delete(cache.items, item.key)
	cache.memoryBytes -= item.size
	cache.recent.Remove(element)
}

func (cache *PageCache) path(key string, epoch int64) string {
	hash := sha256.Sum256([]byte(key))
	return filepath.Join(cache.directory, fmt.Sprintf("epoch-%020d-%s.cache", epoch, hex.EncodeToString(hash[:16])))
}

func cloneCacheEntry(value CacheEntry) CacheEntry {
	value.Body = append([]byte(nil), value.Body...)
	return value
}
