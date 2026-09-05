package presentation

import (
	"container/list"
	"context"
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
	"sync/atomic"
	"time"
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
	persistSlots   chan struct{}

	mu          sync.RWMutex
	items       map[string]*list.Element
	recent      *list.List
	memoryBytes int64
	lastPruneAt time.Time
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
		persistSlots:   make(chan struct{}, 2),
	}, nil
}

func (cache *PageCache) Get(key string, epoch int64) (CacheEntry, bool) {
	cache.mu.RLock()
	if element, ok := cache.items[key]; ok {
		item := element.Value.(*memoryEntry)
		if item.value.Epoch == epoch {
			value := cloneCacheEntry(item.value)
			cache.mu.RUnlock()
			return value, true
		}
	}
	cache.mu.RUnlock()
	// Recency updates are intentionally omitted from the hot path. A stale
	// epoch entry is removed opportunistically under the write lock below;
	// avoiding a global list mutation keeps concurrent cache hits read-only.
	cache.mu.Lock()
	if element, ok := cache.items[key]; ok && element.Value.(*memoryEntry).value.Epoch != epoch {
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
	return cache.persist(value)
}

// PutAsync publishes to the bounded in-memory cache immediately and schedules
// best-effort disk persistence. A full persistence budget is intentionally
// treated as a cache miss on the next process start rather than making a
// public request wait on filesystem I/O.
func (cache *PageCache) PutAsync(value CacheEntry) error {
	if value.Key == "" || value.Epoch < 1 || len(value.Body) > maxCacheFileBytes {
		return fmt.Errorf("invalid page cache entry")
	}
	value = cloneCacheEntry(value)
	cache.addMemory(value.Key, value)
	select {
	case cache.persistSlots <- struct{}{}:
		go func() {
			defer func() { <-cache.persistSlots }()
			_ = cache.persist(value)
		}()
	default:
	}
	return nil
}

func (cache *PageCache) persist(value CacheEntry) error {

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

// PruneDue atomically throttles disk cleanup. The actual bounded scan is kept
// outside the cache lock and can be run by the caller in a background
// goroutine, so a public cache miss never performs a full directory scan.
func (cache *PageCache) PruneDue(now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cache.lastPruneAt.IsZero() && now.Sub(cache.lastPruneAt) < interval {
		return false
	}
	cache.lastPruneAt = now
	return true
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

type RenderFlightStats struct {
	Leaders  uint64
	Waiters  uint64
	Bypassed uint64
	Errors   uint64
}

type renderFlightCall struct {
	done         chan struct{}
	body         []byte
	lastModified string
	err          error
}

// RenderFlight coalesces concurrent misses for one public cache key. It is
// deliberately separate from PageCache: it never stores a failed render and
// its bounded map cannot become a second unbounded cache.
type RenderFlight struct {
	mu       sync.Mutex
	max      int
	calls    map[string]*renderFlightCall
	leaders  atomic.Uint64
	waiters  atomic.Uint64
	bypassed atomic.Uint64
	errors   atomic.Uint64
}

func NewRenderFlight(maxEntries int) *RenderFlight {
	if maxEntries < 1 {
		maxEntries = 32
	}
	if maxEntries > 128 {
		maxEntries = 128
	}
	return &RenderFlight{max: maxEntries, calls: make(map[string]*renderFlightCall, maxEntries)}
}

// Do returns coalesced=true to followers. Leaders execute with a bounded,
// client-independent context so one disconnected request cannot cancel all
// other waiters.
func (flight *RenderFlight) Do(ctx context.Context, key string, render func(context.Context) ([]byte, string, error)) (body []byte, lastModified string, coalesced bool, err error) {
	if flight == nil || render == nil {
		return nil, "", false, fmt.Errorf("render flight is not configured")
	}
	flight.mu.Lock()
	if call, ok := flight.calls[key]; ok {
		flight.waiters.Add(1)
		flight.mu.Unlock()
		select {
		case <-call.done:
			return append([]byte(nil), call.body...), call.lastModified, true, call.err
		case <-ctx.Done():
			return nil, "", true, ctx.Err()
		}
	}
	if len(flight.calls) >= flight.max {
		flight.bypassed.Add(1)
		flight.mu.Unlock()
		body, lastModified, err := runRenderWithDeadline(ctx, render)
		return body, lastModified, false, err
	}
	call := &renderFlightCall{done: make(chan struct{})}
	flight.calls[key] = call
	flight.leaders.Add(1)
	flight.mu.Unlock()

	body, lastModified, err = runRenderWithDeadline(ctx, render)
	if err != nil {
		flight.errors.Add(1)
	}
	flight.mu.Lock()
	call.body = append([]byte(nil), body...)
	call.lastModified = lastModified
	call.err = err
	delete(flight.calls, key)
	close(call.done)
	flight.mu.Unlock()
	return body, lastModified, false, err
}

func (flight *RenderFlight) Stats() RenderFlightStats {
	if flight == nil {
		return RenderFlightStats{}
	}
	return RenderFlightStats{Leaders: flight.leaders.Load(), Waiters: flight.waiters.Load(), Bypassed: flight.bypassed.Load(), Errors: flight.errors.Load()}
}

func runRenderWithDeadline(ctx context.Context, render func(context.Context) ([]byte, string, error)) (body []byte, lastModified string, err error) {
	deadline := 30 * time.Second
	if remaining, ok := ctx.Deadline(); ok && time.Until(remaining) < deadline {
		deadline = time.Until(remaining)
	}
	if deadline <= 0 {
		deadline = time.Second
	}
	renderContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), deadline)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("public render panic: %v", recovered)
			body = nil
			lastModified = ""
		}
	}()
	return render(renderContext)
}
