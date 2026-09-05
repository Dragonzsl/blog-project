package presentation

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

const (
	searchRenderCacheMaxEntries = 64
	searchRenderCacheMaxBytes   = 4 << 20
)

type searchRenderCache struct {
	mu          sync.RWMutex
	items       map[string]*list.Element
	recent      *list.List
	memoryBytes int64
}

type searchRenderCacheEntry struct {
	key   string
	epoch int64
	body  []byte
	size  int64
}

func newSearchRenderCache() *searchRenderCache {
	return &searchRenderCache{
		items:  make(map[string]*list.Element, searchRenderCacheMaxEntries),
		recent: list.New(),
	}
}

func searchRenderKey(themeVersion string, values ...string) string {
	hash := sha256.New()
	for _, value := range append([]string{themeVersion}, values...) {
		_, _ = fmt.Fprintf(hash, "%d:", len(value))
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (cache *searchRenderCache) Get(key string, epoch int64) ([]byte, bool) {
	if cache == nil {
		return nil, false
	}
	cache.mu.RLock()
	element, ok := cache.items[key]
	if !ok || element.Value.(*searchRenderCacheEntry).epoch != epoch {
		cache.mu.RUnlock()
		return nil, false
	}
	body := append([]byte(nil), element.Value.(*searchRenderCacheEntry).body...)
	cache.mu.RUnlock()
	return body, true
}

func (cache *searchRenderCache) Put(key string, epoch int64, body []byte) {
	if cache == nil || key == "" || epoch < 1 || len(body) == 0 {
		return
	}
	size := int64(len(key) + len(body) + 64)
	if size > searchRenderCacheMaxBytes {
		return
	}
	value := &searchRenderCacheEntry{key: key, epoch: epoch, body: append([]byte(nil), body...), size: size}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if existing, ok := cache.items[key]; ok {
		cache.memoryBytes -= existing.Value.(*searchRenderCacheEntry).size
		cache.recent.Remove(existing)
		delete(cache.items, key)
	}
	element := cache.recent.PushFront(value)
	cache.items[key] = element
	cache.memoryBytes += size
	for cache.recent.Len() > searchRenderCacheMaxEntries || cache.memoryBytes > searchRenderCacheMaxBytes {
		oldest := cache.recent.Back()
		if oldest == nil {
			break
		}
		entry := oldest.Value.(*searchRenderCacheEntry)
		cache.memoryBytes -= entry.size
		delete(cache.items, entry.key)
		cache.recent.Remove(oldest)
	}
}
